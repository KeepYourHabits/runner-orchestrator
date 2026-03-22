# Runner Orchestrator

Autoscaling GitHub Actions self-hosted runners for macOS.

## Overview

This orchestrator daemon uses GitHub's official [Scale Set API](https://github.com/actions/scaleset) to dynamically manage self-hosted runners based on job demand.

## Features

- **Autoscaling**: Spawns 0-N runners based on queued jobs
- **Native macOS**: No containers or nested virtualization required
- **Ephemeral runners**: Each job gets a fresh runner instance
- **Auto-recovery**: Handles runner crashes and failures
- **LaunchDaemon**: Runs as a system service on boot

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│  macOS VM                                               │
│  ┌─────────────────────────────────────────────────────┐│
│  │  Runner Orchestrator Daemon                         ││
│  │  - Polls GitHub API for job demand                  ││
│  │  - Spawns runner processes with JIT configs         ││
│  │  - Monitors runner health                           ││
│  └─────────────────────────────────────────────────────┘│
│           │                    │                        │
│           ▼                    ▼                        │
│  ┌────────────────┐   ┌────────────────┐               │
│  │ Runner Process │   │ Runner Process │   (ephemeral) │
│  └────────────────┘   └────────────────┘               │
└─────────────────────────────────────────────────────────┘
```

## Prerequisites

- macOS 11.0 or later
- Go 1.25+
- GitHub Actions runner binary downloaded
- GitHub PAT with `admin:org` scope (or GitHub App)

## Configuration

Set environment variables:

```bash
export GITHUB_TOKEN="ghp_xxxxx"           # PAT with admin:org scope
export GITHUB_ORG="KeepYourHabits"        # Your org name
export RUNNER_DIR="/Users/adosz/actions-runner"  # Path to runner
export MAX_RUNNERS="2"                     # Max concurrent runners
export MIN_RUNNERS="0"                     # Min idle runners
export SCALE_SET_NAME="macos-orchestrated" # Label for workflows
```

## Usage

```bash
# Build
go build -o runner-orchestrator .

# Run
./runner-orchestrator

# Or with flags
./runner-orchestrator \
  --token "$GITHUB_TOKEN" \
  --org "KeepYourHabits" \
  --runner-dir "/Users/adosz/actions-runner" \
  --max-runners 2 \
  --min-runners 0 \
  --scale-set-name "macos-orchestrated"
```

## Workflow Configuration

Use the scale set name in your workflow:

```yaml
jobs:
  build:
    runs-on: macos-orchestrated
    steps:
      - uses: actions/checkout@v4
      # ...
```

## Installation as LaunchDaemon

```bash
# Copy plist to LaunchDaemons
sudo cp com.keepyourhabits.runner-orchestrator.plist /Library/LaunchDaemons/

# Load and start
sudo launchctl load /Library/LaunchDaemons/com.keepyourhabits.runner-orchestrator.plist
```

## License

MIT
