# Skip Members Flag Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add `--skip-members` flag to resume command to skip channel membership fetching for faster incremental updates.

**Architecture:** Add flag to resume command → pass through control.Flags → add stream Option → conditionally use `procChannelInfo` instead of `procChannelInfoWithUsers` in channel worker.

**Tech Stack:** Go, gomock for testing

---

## Task 1: Add Stream Option

**Files:**
- Modify: `stream/stream.go`

**Step 1: Add skipMembers field to Stream struct**

In `stream/stream.go`, add field to Stream struct (around line 44):

```go
type Stream struct {
	oldest, latest time.Time
	client         client.Slack
	limits         rateLimits
	chanCache      *chanCache
	fastSearch     bool
	inclusive      bool
	failChnlNotFnd bool // if true, will fail if channel not found
	skipMembers    bool // if true, skip fetching channel members
	resultFn       []func(sr Result) error
}
```

**Step 2: Add OptSkipMembers function**

Add after `OptFailOnChanNotFound` (around line 183):

```go
// OptSkipMembers skips fetching channel members when set to true.
// Useful for resume operations where members are already in the database.
func OptSkipMembers(b bool) Option {
	return func(s *Stream) {
		s.skipMembers = b
	}
}
```

**Step 3: Run existing tests to ensure no breakage**

Run: `go test ./stream/... -v -count=1`
Expected: All existing tests pass

**Step 4: Commit**

```bash
git add stream/stream.go
git commit -m "feat(stream): add OptSkipMembers option"
```

---

## Task 2: Use skipMembers in Channel Worker

**Files:**
- Modify: `stream/stream_workers.go`

**Step 1: Modify channelWorker to check skipMembers**

In `stream/stream_workers.go`, modify `channelWorker` function (around line 28):

Change from:
```go
channel, err := cs.procChannelInfoWithUsers(ctx, proc, req.sl.Channel, req.sl.ThreadTS)
```

To:
```go
var channel *slack.Channel
var err error
if cs.skipMembers {
	channel, err = cs.procChannelInfo(ctx, proc, req.sl.Channel, req.sl.ThreadTS)
} else {
	channel, err = cs.procChannelInfoWithUsers(ctx, proc, req.sl.Channel, req.sl.ThreadTS)
}
```

**Step 2: Run stream tests**

Run: `go test ./stream/... -v -count=1`
Expected: All tests pass

**Step 3: Commit**

```bash
git add stream/stream_workers.go
git commit -m "feat(stream): use skipMembers flag in channelWorker"
```

---

## Task 3: Add SkipMembers to control.Flags

**Files:**
- Modify: `internal/chunk/control/runner.go`

**Step 1: Add SkipMembers field to Flags struct**

In `internal/chunk/control/runner.go`, add to Flags struct (around line 35):

```go
// SkipMembers skips fetching channel membership during streaming.
// Useful for resume when members are already in the database.
SkipMembers bool
```

**Step 2: Run control tests**

Run: `go test ./internal/chunk/control/... -v -count=1`
Expected: All tests pass

**Step 3: Commit**

```bash
git add internal/chunk/control/runner.go
git commit -m "feat(control): add SkipMembers to Flags struct"
```

---

## Task 4: Pass SkipMembers to Stream in Controller

**Files:**
- Modify: `internal/chunk/control/control.go`

**Step 1: Find where stream options are passed**

Look for where `stream.New` or stream options are configured and add `stream.OptSkipMembers(c.flags.SkipMembers)`.

In `internal/chunk/control/control.go`, find the `New` function and where `c.s` (the stream) is set. Add the option there.

The stream is created in the Controller, likely via options passed to it. Find where `c.streamOpts` or similar is used and ensure `OptSkipMembers` is included.

**Step 2: Locate stream creation**

Run: `grep -n "stream.New\|stream.Opt" internal/chunk/control/*.go`

Add `stream.OptSkipMembers(c.flags.SkipMembers)` to the stream options slice where other options are passed.

**Step 3: Run control tests**

Run: `go test ./internal/chunk/control/... -v -count=1`
Expected: All tests pass

**Step 4: Commit**

```bash
git add internal/chunk/control/control.go
git commit -m "feat(control): pass SkipMembers flag to stream"
```

---

## Task 5: Add Flag to Resume Command

**Files:**
- Modify: `cmd/slackdump/internal/resume/resume.go`

**Step 1: Add SkipMembers to ResumeParams struct**

Around line 52, add:

```go
// SkipMembers skips fetching channel membership during resume.
// Members are typically already in the database from the initial archive.
SkipMembers bool
```

**Step 2: Register the flag in init()**

Around line 68, add:

```go
CmdResume.Flag.BoolVar(&resumeFlags.SkipMembers, "skip-members", false, "skip fetching channel membership (faster resume)")
```

**Step 3: Pass flag to control.Flags in runResume**

Find where `control.Flags` is created (around line 118) and add:

```go
cf := control.Flags{
	Refresh:      resumeFlags.Refresh,
	ChannelUsers: cfg.OnlyChannelUsers,
	SkipMembers:  resumeFlags.SkipMembers,
}
```

**Step 4: Run resume tests**

Run: `go test ./cmd/slackdump/internal/resume/... -v -count=1`
Expected: All tests pass

**Step 5: Commit**

```bash
git add cmd/slackdump/internal/resume/resume.go
git commit -m "feat(resume): add --skip-members flag"
```

---

## Task 6: Update Man Page

**Files:**
- Modify: `slackdump.1`

**Step 1: Add flag documentation**

Find the resume section and add documentation for the new flag:

```
.It Fl skip-members
Skip fetching channel membership during resume. This significantly speeds up
resume operations when members are already in the database from the initial
archive.
```

**Step 2: Commit**

```bash
git add slackdump.1
git commit -m "docs: add --skip-members to man page"
```

---

## Task 7: Integration Test

**Step 1: Build and test manually**

```bash
go build -o slackdump ./cmd/slackdump
./slackdump resume --help
```

Verify `--skip-members` appears in help output.

**Step 2: Test with real database (if available)**

```bash
./slackdump resume --skip-members /path/to/existing/archive
```

Verify:
- No ChannelUsers chunks are created
- Messages are still fetched correctly
- Resume completes faster

**Step 3: Final commit if any fixes needed**

---

## Task 8: Prepare PR

**Step 1: Ensure all tests pass**

```bash
make test
```

**Step 2: Check for any linting issues**

```bash
go vet ./...
```

**Step 3: Create PR description**

Title: `feat(resume): add --skip-members flag to skip channel membership fetching`

Body:
```
## Summary
Adds `--skip-members` flag to the resume command that skips fetching channel
membership during incremental updates. This significantly speeds up resume
operations on large workspaces where membership data is already present from
the initial archive.

## Motivation
On large Slack workspaces with many channels and members, the resume command
spends most of its time re-fetching channel membership even when only checking
for new messages. This flag allows users to skip that step when they don't
need updated membership data.

## Changes
- Add `OptSkipMembers` option to stream package
- Add `SkipMembers` flag to control.Flags
- Add `--skip-members` CLI flag to resume command
- Update man page documentation

## Test plan
- [x] Existing tests pass
- [x] Manual test: flag appears in `--help`
- [x] Manual test: resume with flag skips ChannelUsers chunks
- [x] Manual test: messages still fetched correctly
```
