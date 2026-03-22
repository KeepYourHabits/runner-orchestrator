package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// RunnerManager handles spawning and managing runner processes
type RunnerManager struct {
	runnerDir string
	logger    *slog.Logger
	counter   atomic.Int32
}

// RunnerProcess represents a running runner instance
type RunnerProcess struct {
	Name      string
	WorkDir   string
	cmd       *exec.Cmd
	completed atomic.Bool
	done      chan struct{}
	mu        sync.Mutex
}

// NewRunnerManager creates a new runner manager
func NewRunnerManager(runnerDir string, logger *slog.Logger) (*RunnerManager, error) {
	// Verify runner directory exists
	runScript := filepath.Join(runnerDir, "run.sh")
	if _, err := os.Stat(runScript); os.IsNotExist(err) {
		return nil, fmt.Errorf("runner script not found: %s", runScript)
	}

	return &RunnerManager{
		runnerDir: runnerDir,
		logger:    logger,
	}, nil
}

// SpawnRunner creates and starts a new runner process with JIT config
func (rm *RunnerManager) SpawnRunner(ctx context.Context, runnerName string, encodedJITConfig string) (*RunnerProcess, error) {
	// Generate unique work directory
	id := rm.counter.Add(1)
	workDir := filepath.Join(rm.runnerDir, fmt.Sprintf("_work_%d", id))
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create work directory: %w", err)
	}

	// Build command to run the runner with JIT config
	// The runner supports --jitconfig flag which takes the base64-encoded config directly
	cmd := exec.CommandContext(ctx, filepath.Join(rm.runnerDir, "run.sh"),
		"--jitconfig", encodedJITConfig,
	)

	cmd.Dir = rm.runnerDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("RUNNER_WORK_FOLDER=%s", workDir),
		"RUNNER_ALLOW_RUNASROOT=1",
	)

	// Capture output for debugging
	cmd.Stdout = rm.newLogWriter(runnerName, "stdout")
	cmd.Stderr = rm.newLogWriter(runnerName, "stderr")

	// Start in its own process group
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	rm.logger.Info("Starting runner", "name", runnerName, "workDir", workDir)

	if err := cmd.Start(); err != nil {
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("failed to start runner: %w", err)
	}

	runner := &RunnerProcess{
		Name:    runnerName,
		WorkDir: workDir,
		cmd:     cmd,
		done:    make(chan struct{}),
	}

	return runner, nil
}

// PID returns the process ID of the runner
func (rp *RunnerProcess) PID() int {
	if rp.cmd != nil && rp.cmd.Process != nil {
		return rp.cmd.Process.Pid
	}
	return 0
}

// Wait waits for the runner process to exit
func (rp *RunnerProcess) Wait() error {
	err := rp.cmd.Wait()
	close(rp.done)

	// Cleanup work directory
	if rp.WorkDir != "" {
		os.RemoveAll(rp.WorkDir)
	}

	return err
}

// Stop terminates the runner process
func (rp *RunnerProcess) Stop() {
	rp.mu.Lock()
	defer rp.mu.Unlock()

	if rp.cmd == nil || rp.cmd.Process == nil {
		return
	}

	// Send SIGTERM first
	rp.cmd.Process.Signal(syscall.SIGTERM)

	// Wait up to 10 seconds for graceful shutdown
	select {
	case <-rp.done:
		return
	case <-time.After(10 * time.Second):
		// Force kill if still running
		rp.cmd.Process.Kill()
	}
}

// MarkCompleted marks the runner as having completed its job
func (rp *RunnerProcess) MarkCompleted() {
	rp.completed.Store(true)
}

// IsCompleted returns whether the runner completed its job
func (rp *RunnerProcess) IsCompleted() bool {
	return rp.completed.Load()
}

// logWriter wraps output with runner identification
type logWriter struct {
	logger     *slog.Logger
	runnerName string
	stream     string
}

func (rm *RunnerManager) newLogWriter(runnerName, stream string) *logWriter {
	return &logWriter{
		logger:     rm.logger,
		runnerName: runnerName,
		stream:     stream,
	}
}

func (lw *logWriter) Write(p []byte) (n int, err error) {
	lw.logger.Debug("runner output",
		"runner", lw.runnerName,
		"stream", lw.stream,
		"output", string(p),
	)
	return len(p), nil
}
