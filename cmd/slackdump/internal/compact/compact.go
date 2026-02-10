package compact

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/rusq/slackdump/v3/cmd/slackdump/internal/cfg"
	"github.com/rusq/slackdump/v3/cmd/slackdump/internal/golang/base"
	"github.com/rusq/slackdump/v3/internal/chunk/backend/dbase/repository"
	"github.com/rusq/slackdump/v3/source"
)

//go:embed assets/compact.md
var mdCompact string

var CmdCompact = &base.Command{
	Run:        runCompact,
	UsageLine:  "slackdump compact [flags] <archive directory>",
	Short:      "remove duplicate data from archive database",
	Long:       mdCompact,
	PrintFlags: true,
	FlagMask:   cfg.OmitAll,
	HideWizard: true,
}

var flags struct {
	dryRun   bool
	noBackup bool
}

func init() {
	CmdCompact.Flag.BoolVar(&flags.dryRun, "n", false, "dry run: show what would be removed")
	CmdCompact.Flag.BoolVar(&flags.dryRun, "dry-run", false, "dry run: show what would be removed")
	CmdCompact.Flag.BoolVar(&flags.noBackup, "no-backup", false, "skip creating a backup before compacting")
}

// Stats tracks the number of rows removed per table.
type Stats struct {
	Messages       int64
	Channels       int64
	Users          int64
	Files          int64
	ChannelUsers   int64
	Workspaces     int64
	SearchMessages int64
	SearchFiles    int64
	Chunks         int64
	Sessions       int64
}

func (s *Stats) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Messages:        %d\n", s.Messages)
	fmt.Fprintf(&b, "Channels:        %d\n", s.Channels)
	fmt.Fprintf(&b, "Users:           %d\n", s.Users)
	fmt.Fprintf(&b, "Files:           %d\n", s.Files)
	fmt.Fprintf(&b, "Channel users:   %d\n", s.ChannelUsers)
	fmt.Fprintf(&b, "Workspaces:      %d\n", s.Workspaces)
	fmt.Fprintf(&b, "Search messages: %d\n", s.SearchMessages)
	fmt.Fprintf(&b, "Search files:    %d\n", s.SearchFiles)
	fmt.Fprintf(&b, "Chunks:          %d\n", s.Chunks)
	fmt.Fprintf(&b, "Sessions:        %d\n", s.Sessions)
	return b.String()
}

func (s *Stats) total() int64 {
	return s.Messages + s.Channels + s.Users + s.Files +
		s.ChannelUsers + s.Workspaces + s.SearchMessages + s.SearchFiles +
		s.Chunks + s.Sessions
}

func runCompact(ctx context.Context, cmd *base.Command, args []string) error {
	if len(args) < 1 {
		base.SetExitStatus(base.SInvalidParameters)
		return errors.New("archive directory is required")
	}

	dir := args[0]
	dbpath := filepath.Join(dir, source.DefaultDBFile)

	if _, err := os.Stat(dbpath); err != nil {
		base.SetExitStatus(base.SInvalidParameters)
		return fmt.Errorf("database not found: %w", err)
	}

	if !flags.noBackup && !flags.dryRun {
		bakPath, err := backupDatabase(dbpath)
		if err != nil {
			base.SetExitStatus(base.SApplicationError)
			return fmt.Errorf("backup failed: %w", err)
		}
		slog.Info("backup created", "path", bakPath)
	}

	db, err := sqlx.Open(repository.Driver, dbpath)
	if err != nil {
		base.SetExitStatus(base.SApplicationError)
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	stats, err := compact(ctx, db, flags.dryRun)
	if err != nil {
		base.SetExitStatus(base.SApplicationError)
		return fmt.Errorf("compact: %w", err)
	}

	if flags.dryRun {
		slog.Info("dry run: rows that would be removed")
	} else {
		slog.Info("compaction complete")
	}
	fmt.Print(stats)

	if stats.total() == 0 {
		slog.Info("database is already compact")
	}

	return nil
}

func backupDatabase(dbpath string) (string, error) {
	bakPath := dbpath + ".bak." + time.Now().Format("20060102_150405")

	src, err := os.Open(dbpath)
	if err != nil {
		return "", err
	}
	defer src.Close()

	dst, err := os.Create(bakPath)
	if err != nil {
		return "", err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return "", err
	}
	return bakPath, nil
}

// dedup describes a single deduplication operation.
type dedup struct {
	name     string
	countSQL string
	delSQL   string
}

// dedups returns the deduplication operations for all entity tables.
// All queries use CTE-based joins instead of correlated subqueries for
// O(n) performance on large databases.
func dedups() []dedup {
	return []dedup{
		{
			// MESSAGE is scoped by chunk TYPE_ID so that the same message ID
			// appearing in both channel (0) and thread (1) chunks is preserved.
			name: "MESSAGE",
			countSQL: `WITH KEEP AS (
				SELECT M.ID, C.TYPE_ID, MAX(M.CHUNK_ID) AS MAX_CID
				FROM MESSAGE M JOIN CHUNK C ON C.ID = M.CHUNK_ID
				GROUP BY M.ID, C.TYPE_ID
			)
			SELECT COUNT(*) FROM MESSAGE M
			JOIN CHUNK C ON C.ID = M.CHUNK_ID
			JOIN KEEP K ON K.ID = M.ID AND K.TYPE_ID = C.TYPE_ID
			WHERE M.CHUNK_ID < K.MAX_CID`,
			delSQL: `DELETE FROM MESSAGE WHERE ROWID IN (
				WITH KEEP AS (
					SELECT M.ID, C.TYPE_ID, MAX(M.CHUNK_ID) AS MAX_CID
					FROM MESSAGE M JOIN CHUNK C ON C.ID = M.CHUNK_ID
					GROUP BY M.ID, C.TYPE_ID
				)
				SELECT M.ROWID FROM MESSAGE M
				JOIN CHUNK C ON C.ID = M.CHUNK_ID
				JOIN KEEP K ON K.ID = M.ID AND K.TYPE_ID = C.TYPE_ID
				WHERE M.CHUNK_ID < K.MAX_CID
			)`,
		},
		{
			name: "CHANNEL",
			countSQL: `WITH KEEP AS (
				SELECT ID, MAX(CHUNK_ID) AS MAX_CID FROM CHANNEL GROUP BY ID
			)
			SELECT COUNT(*) FROM CHANNEL T
			JOIN KEEP K ON K.ID = T.ID
			WHERE T.CHUNK_ID < K.MAX_CID`,
			delSQL: `DELETE FROM CHANNEL WHERE ROWID IN (
				WITH KEEP AS (
					SELECT ID, MAX(CHUNK_ID) AS MAX_CID FROM CHANNEL GROUP BY ID
				)
				SELECT T.ROWID FROM CHANNEL T
				JOIN KEEP K ON K.ID = T.ID
				WHERE T.CHUNK_ID < K.MAX_CID
			)`,
		},
		{
			name: "S_USER",
			countSQL: `WITH KEEP AS (
				SELECT ID, MAX(CHUNK_ID) AS MAX_CID FROM S_USER GROUP BY ID
			)
			SELECT COUNT(*) FROM S_USER T
			JOIN KEEP K ON K.ID = T.ID
			WHERE T.CHUNK_ID < K.MAX_CID`,
			delSQL: `DELETE FROM S_USER WHERE ROWID IN (
				WITH KEEP AS (
					SELECT ID, MAX(CHUNK_ID) AS MAX_CID FROM S_USER GROUP BY ID
				)
				SELECT T.ROWID FROM S_USER T
				JOIN KEEP K ON K.ID = T.ID
				WHERE T.CHUNK_ID < K.MAX_CID
			)`,
		},
		{
			name: "FILE",
			countSQL: `WITH KEEP AS (
				SELECT ID, MAX(CHUNK_ID) AS MAX_CID FROM FILE GROUP BY ID
			)
			SELECT COUNT(*) FROM FILE T
			JOIN KEEP K ON K.ID = T.ID
			WHERE T.CHUNK_ID < K.MAX_CID`,
			delSQL: `DELETE FROM FILE WHERE ROWID IN (
				WITH KEEP AS (
					SELECT ID, MAX(CHUNK_ID) AS MAX_CID FROM FILE GROUP BY ID
				)
				SELECT T.ROWID FROM FILE T
				JOIN KEEP K ON K.ID = T.ID
				WHERE T.CHUNK_ID < K.MAX_CID
			)`,
		},
		{
			name: "CHANNEL_USER",
			countSQL: `WITH KEEP AS (
				SELECT CHANNEL_ID, MAX(CHUNK_ID) AS MAX_CID FROM CHANNEL_USER GROUP BY CHANNEL_ID
			)
			SELECT COUNT(*) FROM CHANNEL_USER T
			JOIN KEEP K ON K.CHANNEL_ID = T.CHANNEL_ID
			WHERE T.CHUNK_ID < K.MAX_CID`,
			delSQL: `DELETE FROM CHANNEL_USER WHERE ROWID IN (
				WITH KEEP AS (
					SELECT CHANNEL_ID, MAX(CHUNK_ID) AS MAX_CID FROM CHANNEL_USER GROUP BY CHANNEL_ID
				)
				SELECT T.ROWID FROM CHANNEL_USER T
				JOIN KEEP K ON K.CHANNEL_ID = T.CHANNEL_ID
				WHERE T.CHUNK_ID < K.MAX_CID
			)`,
		},
		{
			name: "WORKSPACE",
			countSQL: `WITH KEEP AS (
				SELECT TEAM_ID, MAX(CHUNK_ID) AS MAX_CID FROM WORKSPACE GROUP BY TEAM_ID
			)
			SELECT COUNT(*) FROM WORKSPACE T
			JOIN KEEP K ON K.TEAM_ID = T.TEAM_ID
			WHERE T.CHUNK_ID < K.MAX_CID`,
			delSQL: `DELETE FROM WORKSPACE WHERE ROWID IN (
				WITH KEEP AS (
					SELECT TEAM_ID, MAX(CHUNK_ID) AS MAX_CID FROM WORKSPACE GROUP BY TEAM_ID
				)
				SELECT T.ROWID FROM WORKSPACE T
				JOIN KEEP K ON K.TEAM_ID = T.TEAM_ID
				WHERE T.CHUNK_ID < K.MAX_CID
			)`,
		},
		{
			name: "SEARCH_MESSAGE",
			countSQL: `WITH KEEP AS (
				SELECT CHANNEL_ID, MAX(CHUNK_ID) AS MAX_CID FROM SEARCH_MESSAGE GROUP BY CHANNEL_ID
			)
			SELECT COUNT(*) FROM SEARCH_MESSAGE T
			JOIN KEEP K ON K.CHANNEL_ID = T.CHANNEL_ID
			WHERE T.CHUNK_ID < K.MAX_CID`,
			delSQL: `DELETE FROM SEARCH_MESSAGE WHERE ROWID IN (
				WITH KEEP AS (
					SELECT CHANNEL_ID, MAX(CHUNK_ID) AS MAX_CID FROM SEARCH_MESSAGE GROUP BY CHANNEL_ID
				)
				SELECT T.ROWID FROM SEARCH_MESSAGE T
				JOIN KEEP K ON K.CHANNEL_ID = T.CHANNEL_ID
				WHERE T.CHUNK_ID < K.MAX_CID
			)`,
		},
		{
			name: "SEARCH_FILE",
			countSQL: `WITH KEEP AS (
				SELECT FILE_ID, MAX(CHUNK_ID) AS MAX_CID FROM SEARCH_FILE GROUP BY FILE_ID
			)
			SELECT COUNT(*) FROM SEARCH_FILE T
			JOIN KEEP K ON K.FILE_ID = T.FILE_ID
			WHERE T.CHUNK_ID < K.MAX_CID`,
			delSQL: `DELETE FROM SEARCH_FILE WHERE ROWID IN (
				WITH KEEP AS (
					SELECT FILE_ID, MAX(CHUNK_ID) AS MAX_CID FROM SEARCH_FILE GROUP BY FILE_ID
				)
				SELECT T.ROWID FROM SEARCH_FILE T
				JOIN KEEP K ON K.FILE_ID = T.FILE_ID
				WHERE T.CHUNK_ID < K.MAX_CID
			)`,
		},
	}
}

const (
	orphanChunksCountSQL = `SELECT COUNT(*) FROM CHUNK WHERE ID NOT IN (
		SELECT CHUNK_ID FROM MESSAGE
		UNION SELECT CHUNK_ID FROM CHANNEL
		UNION SELECT CHUNK_ID FROM S_USER
		UNION SELECT CHUNK_ID FROM FILE
		UNION SELECT CHUNK_ID FROM CHANNEL_USER
		UNION SELECT CHUNK_ID FROM WORKSPACE
		UNION SELECT CHUNK_ID FROM SEARCH_MESSAGE
		UNION SELECT CHUNK_ID FROM SEARCH_FILE
	)`
	orphanChunksDelSQL = `DELETE FROM CHUNK WHERE ID NOT IN (
		SELECT CHUNK_ID FROM MESSAGE
		UNION SELECT CHUNK_ID FROM CHANNEL
		UNION SELECT CHUNK_ID FROM S_USER
		UNION SELECT CHUNK_ID FROM FILE
		UNION SELECT CHUNK_ID FROM CHANNEL_USER
		UNION SELECT CHUNK_ID FROM WORKSPACE
		UNION SELECT CHUNK_ID FROM SEARCH_MESSAGE
		UNION SELECT CHUNK_ID FROM SEARCH_FILE
	)`
	orphanSessionsCountSQL = `SELECT COUNT(*) FROM SESSION WHERE ID NOT IN (
		SELECT DISTINCT SESSION_ID FROM CHUNK
	)`
	orphanSessionsDelSQL = `DELETE FROM SESSION WHERE ID NOT IN (
		SELECT DISTINCT SESSION_ID FROM CHUNK
	)`
)

func compact(ctx context.Context, db *sqlx.DB, dryRun bool) (*Stats, error) {
	var stats Stats

	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Deduplicate entity tables.
	for _, d := range dedups() {
		n, err := dedupTable(ctx, tx, d, dryRun)
		if err != nil {
			return nil, fmt.Errorf("dedup %s: %w", d.name, err)
		}
		switch d.name {
		case "MESSAGE":
			stats.Messages = n
		case "CHANNEL":
			stats.Channels = n
		case "S_USER":
			stats.Users = n
		case "FILE":
			stats.Files = n
		case "CHANNEL_USER":
			stats.ChannelUsers = n
		case "WORKSPACE":
			stats.Workspaces = n
		case "SEARCH_MESSAGE":
			stats.SearchMessages = n
		case "SEARCH_FILE":
			stats.SearchFiles = n
		}
		if n > 0 {
			slog.Info("deduplicated", "table", d.name, "rows", n)
		}
	}

	// Remove orphaned chunks.
	chunks, err := cleanOrphans(ctx, tx, orphanChunksCountSQL, orphanChunksDelSQL, dryRun)
	if err != nil {
		return nil, fmt.Errorf("orphan chunks: %w", err)
	}
	stats.Chunks = chunks
	if chunks > 0 {
		slog.Info("removed orphaned chunks", "count", chunks)
	}

	// Remove orphaned sessions.
	sessions, err := cleanOrphans(ctx, tx, orphanSessionsCountSQL, orphanSessionsDelSQL, dryRun)
	if err != nil {
		return nil, fmt.Errorf("orphan sessions: %w", err)
	}
	stats.Sessions = sessions
	if sessions > 0 {
		slog.Info("removed orphaned sessions", "count", sessions)
	}

	if dryRun {
		// Roll back; nothing should change.
		return &stats, nil
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	// VACUUM must run outside a transaction.
	if stats.total() > 0 {
		slog.Info("vacuuming database")
		if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
			return &stats, fmt.Errorf("vacuum: %w", err)
		}
	}

	return &stats, nil
}

func dedupTable(ctx context.Context, tx *sqlx.Tx, d dedup, dryRun bool) (int64, error) {
	if dryRun {
		var n int64
		if err := tx.QueryRowxContext(ctx, d.countSQL).Scan(&n); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, nil
			}
			return 0, err
		}
		return n, nil
	}
	res, err := tx.ExecContext(ctx, d.delSQL)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func cleanOrphans(ctx context.Context, tx *sqlx.Tx, countSQL, delSQL string, dryRun bool) (int64, error) {
	if dryRun {
		var n int64
		if err := tx.QueryRowxContext(ctx, countSQL).Scan(&n); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, nil
			}
			return 0, err
		}
		return n, nil
	}
	res, err := tx.ExecContext(ctx, delSQL)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
