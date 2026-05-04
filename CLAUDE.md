# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Slackdump is a Go CLI tool and library for archiving Slack workspaces without admin privileges. It can archive messages, users, channels, files, and emojis, and generate Slack-compatible exports.

## Build and Test Commands

```bash
# Build
make                    # Build slackdump executable
go build -o slackdump ./cmd/slackdump

# Run directly
go run ./cmd/slackdump wiz

# Test (always use race detection)
make test               # Equivalent to: go test -race -cover ./...
go test -race ./path/to/package  # Single package

# Generate mocks and stringer methods
make generate           # Requires mockgen and stringer installed
make install_tools      # Install mockgen and stringer

# Docker validation
make docker_test

# Release build check
make goreleaser
```

## Architecture

### Session Model
The core type is `Session` (slackdump.go) which wraps a Slack client with caching, rate limiting, and filesystem adapters. Create via `New()` or `NewNoValidate()` with functional options pattern.

### Package Structure

**Public API (root level):**
- `auth/` - Authentication providers (token, cookie, browser-based EZ-Login)
- `stream/` - Streaming interface for channel/message iteration
- `export/` - Export format handling
- `downloader/` - File download capabilities
- `processor/` - Message processing pipeline

**Internal packages:**
- `internal/chunk/` - Archive chunk storage with file and SQLite backends; Recorder/Player for stream operations
- `internal/client/` - Slack API client abstraction with enterprise (edge) client support
- `internal/edge/` - Enterprise Grid API client for large workspaces
- `internal/convert/` - Format converters between archive, export, and dump formats
- `internal/network/` - Tier-based rate limiting (Tier2/3/4 per Slack API limits)
- `internal/cache/` - User and channel caching with TTL
- `internal/structures/` - Core data types (conversations, messages, time/URL parsing)
- `internal/viewer/` - Built-in web viewer for exports

**CLI commands** (`cmd/slackdump/internal/`):
- `workspace/` - Workspace/authentication management
- `archive/` - Create archives
- `export/` - Create Slack-compatible exports
- `dump/` - Legacy dump format
- `convert/` - Format conversion
- `wizard/` - Interactive TUI mode

### Data Flow
Archive mode (default) → chunks stored in temp directory → converted to export/dump format on completion. This minimizes memory usage for large workspaces.

### Rate Limiting
Uses tiered rate limiting matching Slack API tiers. Configure via `network.Limits` or use defaults in `DefLimits`.

## Testing Patterns

- Uses `testify/assert` for assertions
- Mocks generated with `go.uber.org/mock/mockgen` (stored in `internal/mocks/`)
- Test fixtures in `internal/fixtures/`
- Table-driven tests with subtests are preferred
- Many tests use `t.Parallel()`

## Key Dependencies

- `github.com/rusq/slack` - Slack API client (fork with modifications)
- `github.com/rusq/slackauth` - Browser-based authentication
- `modernc.org/sqlite` - Pure Go SQLite (no CGO required)
- `github.com/charmbracelet/bubbletea` - TUI framework for wizard mode
- `github.com/pressly/goose` - Database migrations

## Notes

- CGO is disabled in builds (`CGO_ENABLED=0`)
- Enterprise workspaces use the edge client (`internal/edge/`) which calls different API endpoints
- The `chunk` package has obfuscation support for privacy-preserving archives
- Logging uses `log/slog` (standard library structured logging)
