package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
)

// Orchestrator manages runner scaling based on job demand
type Orchestrator struct {
	cfg            *Config
	logger         *slog.Logger
	scalesetClient *scaleset.Client
	scaleSet       *scaleset.RunnerScaleSet
	runnerMgr      *RunnerManager

	mu      sync.Mutex
	runners map[string]*RunnerProcess // runnerName -> process
}

// NewOrchestrator creates a new orchestrator instance
func NewOrchestrator(cfg *Config, logger *slog.Logger) (*Orchestrator, error) {
	// Create scaleset client with PAT
	// GitHubConfigURL should be the org URL: https://github.com/ORG
	githubConfigURL := fmt.Sprintf("https://github.com/%s", cfg.GitHubOrg)

	client, err := scaleset.NewClientWithPersonalAccessToken(
		scaleset.NewClientWithPersonalAccessTokenConfig{
			GitHubConfigURL:     githubConfigURL,
			PersonalAccessToken: cfg.GitHubToken,
			SystemInfo: scaleset.SystemInfo{
				System:    "runner-orchestrator",
				Version:   "1.0.0",
				Subsystem: "orchestrator",
			},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create scaleset client: %w", err)
	}

	// Create runner manager
	runnerMgr, err := NewRunnerManager(cfg.RunnerDir, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create runner manager: %w", err)
	}

	return &Orchestrator{
		cfg:            cfg,
		logger:         logger,
		scalesetClient: client,
		runnerMgr:      runnerMgr,
		runners:        make(map[string]*RunnerProcess),
	}, nil
}

// Run starts the orchestration loop
func (o *Orchestrator) Run(ctx context.Context) error {
	// Create or get the runner scale set
	scaleSet, err := o.scalesetClient.CreateRunnerScaleSet(ctx, &scaleset.RunnerScaleSet{
		Name:          o.cfg.ScaleSetName,
		RunnerGroupID: 1, // Default group
		Labels:        o.buildLabels(),
		RunnerSetting: scaleset.RunnerSetting{
			DisableUpdate: true,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create scale set: %w", err)
	}
	o.scaleSet = scaleSet
	o.logger.Info("Scale set ready", "id", scaleSet.ID, "name", scaleSet.Name)

	// Update system info with scale set ID
	o.scalesetClient.SetSystemInfo(scaleset.SystemInfo{
		System:     "runner-orchestrator",
		Version:    "1.0.0",
		Subsystem:  "orchestrator",
		ScaleSetID: scaleSet.ID,
	})

	// Ensure cleanup on exit
	defer o.cleanup(context.WithoutCancel(ctx))

	// Create message session
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "orchestrator"
	}

	sessionClient, err := o.scalesetClient.MessageSessionClient(ctx, scaleSet.ID, hostname)
	if err != nil {
		return fmt.Errorf("failed to create session client: %w", err)
	}
	defer sessionClient.Close(context.WithoutCancel(ctx))

	// Create listener
	lst, err := listener.New(sessionClient, listener.Config{
		ScaleSetID: scaleSet.ID,
		MaxRunners: o.cfg.MaxRunners,
		Logger:     o.logger.WithGroup("listener"),
	})
	if err != nil {
		return fmt.Errorf("failed to create listener: %w", err)
	}

	// Start listener with our scaler
	o.logger.Info("Starting listener loop")
	return lst.Run(ctx, o)
}

// HandleDesiredRunnerCount implements listener.Scaler - called to adjust runner count
func (o *Orchestrator) HandleDesiredRunnerCount(ctx context.Context, count int) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	desired := count
	current := len(o.runners)

	// Ensure minimum runners
	if desired < o.cfg.MinRunners {
		desired = o.cfg.MinRunners
	}

	// Cap at maximum
	if desired > o.cfg.MaxRunners {
		desired = o.cfg.MaxRunners
	}

	o.logger.Debug("Scaling check",
		"requested", count,
		"current", current,
		"desired", desired,
	)

	// Scale up if needed
	for current < desired {
		if err := o.spawnRunner(ctx); err != nil {
			o.logger.Error("Failed to spawn runner", "error", err)
			break
		}
		current++
	}

	return current, nil
}

// HandleJobStarted implements listener.Scaler - called when a job starts
func (o *Orchestrator) HandleJobStarted(ctx context.Context, msg *scaleset.JobStarted) error {
	o.logger.Info("Job started",
		"jobId", msg.JobID,
		"runnerName", msg.RunnerName,
		"workflow", msg.JobDisplayName,
	)
	return nil
}

// HandleJobCompleted implements listener.Scaler - called when a job completes
func (o *Orchestrator) HandleJobCompleted(ctx context.Context, msg *scaleset.JobCompleted) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.logger.Info("Job completed",
		"jobId", msg.JobID,
		"runnerName", msg.RunnerName,
		"result", msg.Result,
	)

	// Runner will self-terminate (ephemeral), remove from tracking
	if runner, ok := o.runners[msg.RunnerName]; ok {
		runner.MarkCompleted()
		delete(o.runners, msg.RunnerName)
	}

	return nil
}

func (o *Orchestrator) spawnRunner(ctx context.Context) error {
	// Generate JIT config for new runner
	runnerName := fmt.Sprintf("runner-%d", len(o.runners)+1)
	jitConfig, err := o.scalesetClient.GenerateJitRunnerConfig(ctx, &scaleset.RunnerScaleSetJitRunnerSetting{
		Name:       runnerName,
		WorkFolder: "_work",
	}, o.scaleSet.ID)
	if err != nil {
		return fmt.Errorf("failed to generate JIT config: %w", err)
	}

	// Spawn the runner process
	runner, err := o.runnerMgr.SpawnRunner(ctx, runnerName, jitConfig.EncodedJITConfig)
	if err != nil {
		return fmt.Errorf("failed to spawn runner: %w", err)
	}

	o.runners[runner.Name] = runner
	o.logger.Info("Spawned runner", "name", runner.Name, "pid", runner.PID())

	// Monitor runner in background
	go o.monitorRunner(runner)

	return nil
}

func (o *Orchestrator) monitorRunner(runner *RunnerProcess) {
	err := runner.Wait()

	o.mu.Lock()
	defer o.mu.Unlock()

	if err != nil && !runner.IsCompleted() {
		o.logger.Warn("Runner exited unexpectedly", "name", runner.Name, "error", err)
	} else {
		o.logger.Debug("Runner exited normally", "name", runner.Name)
	}

	delete(o.runners, runner.Name)
}

func (o *Orchestrator) buildLabels() []scaleset.Label {
	if len(o.cfg.Labels) == 0 {
		// Default to scale set name if no labels provided
		return []scaleset.Label{{Name: o.cfg.ScaleSetName}}
	}
	labels := make([]scaleset.Label, len(o.cfg.Labels))
	for i, l := range o.cfg.Labels {
		labels[i] = scaleset.Label{Name: l}
	}
	return labels
}

func (o *Orchestrator) cleanup(ctx context.Context) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.logger.Info("Cleaning up runners", "count", len(o.runners))

	// Stop all running runners
	for name, runner := range o.runners {
		o.logger.Debug("Stopping runner", "name", name)
		runner.Stop()
	}

	// Delete the scale set
	if o.scaleSet != nil {
		o.logger.Info("Deleting scale set", "id", o.scaleSet.ID)
		if err := o.scalesetClient.DeleteRunnerScaleSet(ctx, o.scaleSet.ID); err != nil {
			o.logger.Error("Failed to delete scale set", "error", err)
		}
	}
}
