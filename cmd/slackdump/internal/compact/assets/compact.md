# Compact Command

Removes duplicate data from archive databases to reclaim disk space.

## Usage

```bash
slackdump compact [flags] <archive directory>
```

When resuming an archive, Slackdump creates new sessions and chunks, often
re-fetching overlapping data (messages in the lookback window, channel members,
etc.). Because the schema uses composite primary keys like `(ID, CHUNK_ID)`,
the same logical entity can exist in multiple chunks. Over time this inflates
the database.

The `compact` command deduplicates these rows, keeping only the copy from the
most recent chunk for each logical entity. It also removes orphaned chunks and
sessions that no longer reference any data, then VACUUMs the database.

By default a timestamped backup is created before any changes are made.

## Flags

- `-n`, `--dry-run`: Show what would be removed without making changes.
- `--no-backup`: Skip creating a backup before compacting.

## Examples

Preview what would be removed:
```bash
slackdump compact -n ./my-archive/
```

Compact the database (creates a backup first):
```bash
slackdump compact ./my-archive/
```

Compact without backup:
```bash
slackdump compact --no-backup ./my-archive/
```
