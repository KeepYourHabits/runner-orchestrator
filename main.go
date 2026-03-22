package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// Config holds the orchestrator configuration
type Config struct {
	GitHubToken  string
	GitHubOrg    string
	RunnerDir    string
	MaxRunners   int
	MinRunners   int
	ScaleSetName string
	Labels       []string
	LogLevel     string
}

func main() {
	cfg := parseFlags()

	// Set up logging
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	// Validate config
	if err := validateConfig(cfg); err != nil {
		slog.Error("Invalid configuration", "error", err)
		os.Exit(1)
	}

	// Create context with signal handling
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	slog.Info("Starting runner orchestrator",
		"org", cfg.GitHubOrg,
		"maxRunners", cfg.MaxRunners,
		"minRunners", cfg.MinRunners,
		"scaleSetName", cfg.ScaleSetName,
	)

	// Create and run orchestrator
	orch, err := NewOrchestrator(cfg, logger)
	if err != nil {
		slog.Error("Failed to create orchestrator", "error", err)
		os.Exit(1)
	}

	if err := orch.Run(ctx); err != nil && err != context.Canceled {
		slog.Error("Orchestrator failed", "error", err)
		os.Exit(1)
	}

	slog.Info("Orchestrator stopped")
}

func parseFlags() *Config {
	cfg := &Config{}

	flag.StringVar(&cfg.GitHubToken, "token", os.Getenv("GITHUB_TOKEN"), "GitHub PAT with admin:org scope")
	flag.StringVar(&cfg.GitHubOrg, "org", os.Getenv("GITHUB_ORG"), "GitHub organization name")
	flag.StringVar(&cfg.RunnerDir, "runner-dir", os.Getenv("RUNNER_DIR"), "Path to actions-runner directory")
	flag.IntVar(&cfg.MaxRunners, "max-runners", getEnvInt("MAX_RUNNERS", 2), "Maximum concurrent runners")
	flag.IntVar(&cfg.MinRunners, "min-runners", getEnvInt("MIN_RUNNERS", 0), "Minimum idle runners")
	flag.StringVar(&cfg.ScaleSetName, "scale-set-name", getEnvStr("SCALE_SET_NAME", "macos-orchestrated"), "Scale set name (workflow label)")
	flag.StringVar(&cfg.LogLevel, "log-level", getEnvStr("LOG_LEVEL", "info"), "Log level (debug, info, warn, error)")

	flag.Parse()

	// Default labels include the scale set name
	cfg.Labels = []string{cfg.ScaleSetName, "self-hosted", "macOS", "X64"}

	return cfg
}

func validateConfig(cfg *Config) error {
	if cfg.GitHubToken == "" {
		return errConfigMissing("GITHUB_TOKEN or --token")
	}
	if cfg.GitHubOrg == "" {
		return errConfigMissing("GITHUB_ORG or --org")
	}
	if cfg.RunnerDir == "" {
		return errConfigMissing("RUNNER_DIR or --runner-dir")
	}
	if cfg.MaxRunners < 1 {
		return errConfigInvalid("max-runners must be >= 1")
	}
	if cfg.MinRunners < 0 || cfg.MinRunners > cfg.MaxRunners {
		return errConfigInvalid("min-runners must be >= 0 and <= max-runners")
	}
	return nil
}

func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		var i int
		if _, err := os.Stdin.Read(nil); err == nil {
			return i
		}
	}
	return defaultVal
}

func getEnvStr(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

type configError struct {
	msg string
}

func (e *configError) Error() string {
	return e.msg
}

func errConfigMissing(field string) error {
	return &configError{msg: "missing required config: " + field}
}

func errConfigInvalid(msg string) error {
	return &configError{msg: "invalid config: " + msg}
}
