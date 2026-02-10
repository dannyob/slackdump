package compact

import (
	"context"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/rusq/slackdump/v4/internal/chunk/backend/dbase/repository"
)

func testDB(t *testing.T) *sqlx.DB {
	t.Helper()
	db, err := sqlx.Open(repository.Driver, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	err = repository.Migrate(context.Background(), db.DB, false)
	require.NoError(t, err)

	return db
}

// insertSession inserts a session and returns its ID.
func insertSession(t *testing.T, db *sqlx.DB, id int64) {
	t.Helper()
	_, err := db.Exec("INSERT INTO SESSION (ID, FINISHED, MODE) VALUES (?, 1, 'archive')", id)
	require.NoError(t, err)
}

// insertChunk inserts a chunk with the given type.
func insertChunk(t *testing.T, db *sqlx.DB, id, sessionID int64, typeID int) {
	t.Helper()
	_, err := db.Exec(
		"INSERT INTO CHUNK (ID, SESSION_ID, TYPE_ID, UNIX_TS, NUM_REC) VALUES (?, ?, ?, 0, 0)",
		id, sessionID, typeID,
	)
	require.NoError(t, err)
}

// insertMessage inserts a message row.
func insertMessage(t *testing.T, db *sqlx.DB, msgID, chunkID int64, channelID, ts string) {
	t.Helper()
	_, err := db.Exec(
		"INSERT INTO MESSAGE (ID, CHUNK_ID, CHANNEL_ID, TS, IDX, DATA) VALUES (?, ?, ?, ?, 0, '{}')",
		msgID, chunkID, channelID, ts,
	)
	require.NoError(t, err)
}

// insertChannel inserts a channel row.
func insertChannel(t *testing.T, db *sqlx.DB, chanID string, chunkID int64) {
	t.Helper()
	_, err := db.Exec(
		"INSERT INTO CHANNEL (ID, CHUNK_ID, NAME, IDX, DATA) VALUES (?, ?, 'test', 0, '{}')",
		chanID, chunkID,
	)
	require.NoError(t, err)
}

// insertUser inserts a user row.
func insertUser(t *testing.T, db *sqlx.DB, userID string, chunkID int64) {
	t.Helper()
	_, err := db.Exec(
		"INSERT INTO S_USER (ID, CHUNK_ID, IDX, USERNAME, DATA) VALUES (?, ?, 0, 'testuser', '{}')",
		userID, chunkID,
	)
	require.NoError(t, err)
}

// insertFile inserts a file row.
func insertFile(t *testing.T, db *sqlx.DB, fileID string, chunkID int64, channelID string) {
	t.Helper()
	_, err := db.Exec(
		"INSERT INTO FILE (ID, CHUNK_ID, CHANNEL_ID, IDX, MODE, DATA) VALUES (?, ?, ?, 0, 'hosted', '{}')",
		fileID, chunkID, channelID,
	)
	require.NoError(t, err)
}

// insertChannelUser inserts a channel-user membership row.
func insertChannelUser(t *testing.T, db *sqlx.DB, channelID, userID string, chunkID int64) {
	t.Helper()
	_, err := db.Exec(
		"INSERT INTO CHANNEL_USER (CHANNEL_ID, USER_ID, CHUNK_ID, IDX) VALUES (?, ?, ?, 0)",
		channelID, userID, chunkID,
	)
	require.NoError(t, err)
}

// insertWorkspace inserts a workspace row.
func insertWorkspace(t *testing.T, db *sqlx.DB, chunkID int64, teamID string) {
	t.Helper()
	_, err := db.Exec(
		"INSERT INTO WORKSPACE (CHUNK_ID, TEAM, TEAM_ID, USER_ID, URL, DATA) VALUES (?, 'team', ?, 'U1', 'https://x.slack.com', '{}')",
		chunkID, teamID,
	)
	require.NoError(t, err)
}

// countRows returns the number of rows in the given table.
func countRows(t *testing.T, db *sqlx.DB, table string) int64 {
	t.Helper()
	var n int64
	err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n)
	require.NoError(t, err)
	return n
}

func TestCompact_NoDuplicates(t *testing.T) {
	db := testDB(t)

	insertSession(t, db, 1)
	insertChunk(t, db, 1, 1, 0) // TYPE_ID=0 (channel)
	insertMessage(t, db, 100, 1, "C1", "1.0")
	insertChannel(t, db, "C1", 1)
	insertUser(t, db, "U1", 1)

	stats, err := compact(context.Background(), db, false)
	require.NoError(t, err)

	assert.Equal(t, int64(0), stats.total(), "nothing should be removed")
	assert.Equal(t, int64(1), countRows(t, db, "MESSAGE"))
	assert.Equal(t, int64(1), countRows(t, db, "CHANNEL"))
	assert.Equal(t, int64(1), countRows(t, db, "S_USER"))
}

func TestCompact_MessageDedup(t *testing.T) {
	db := testDB(t)

	insertSession(t, db, 1)
	insertChunk(t, db, 1, 1, 0) // older chunk
	insertChunk(t, db, 2, 1, 0) // newer chunk
	// Same message ID=100 in both chunks (TYPE_ID=0).
	insertMessage(t, db, 100, 1, "C1", "1.0")
	insertMessage(t, db, 100, 2, "C1", "1.0")

	require.Equal(t, int64(2), countRows(t, db, "MESSAGE"))

	stats, err := compact(context.Background(), db, false)
	require.NoError(t, err)

	assert.Equal(t, int64(1), stats.Messages)
	assert.Equal(t, int64(1), countRows(t, db, "MESSAGE"))

	// The surviving message should be from chunk 2 (the latest).
	var chunkID int64
	err = db.QueryRow("SELECT CHUNK_ID FROM MESSAGE WHERE ID = 100").Scan(&chunkID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), chunkID)
}

func TestCompact_ThreadMessageDedup(t *testing.T) {
	db := testDB(t)

	insertSession(t, db, 1)
	insertChunk(t, db, 1, 1, 1) // TYPE_ID=1 (thread), older
	insertChunk(t, db, 2, 1, 1) // TYPE_ID=1, newer
	insertMessage(t, db, 200, 1, "C1", "2.0")
	insertMessage(t, db, 200, 2, "C1", "2.0")

	stats, err := compact(context.Background(), db, false)
	require.NoError(t, err)

	assert.Equal(t, int64(1), stats.Messages)
	assert.Equal(t, int64(1), countRows(t, db, "MESSAGE"))
}

func TestCompact_CrossTypePreservation(t *testing.T) {
	db := testDB(t)

	insertSession(t, db, 1)
	insertChunk(t, db, 1, 1, 0) // TYPE_ID=0 (channel)
	insertChunk(t, db, 2, 1, 1) // TYPE_ID=1 (thread)
	// Same message ID=100 in both chunk types — both should survive.
	insertMessage(t, db, 100, 1, "C1", "1.0")
	insertMessage(t, db, 100, 2, "C1", "1.0")

	stats, err := compact(context.Background(), db, false)
	require.NoError(t, err)

	assert.Equal(t, int64(0), stats.Messages, "cross-type messages should both survive")
	assert.Equal(t, int64(2), countRows(t, db, "MESSAGE"))
}

func TestCompact_ChannelUserDedup(t *testing.T) {
	db := testDB(t)

	insertSession(t, db, 1)
	insertChunk(t, db, 1, 1, 0)
	insertChunk(t, db, 2, 1, 0)
	// Same (CHANNEL_ID, USER_ID) in two chunks.
	insertChannelUser(t, db, "C1", "U1", 1)
	insertChannelUser(t, db, "C1", "U1", 2)

	stats, err := compact(context.Background(), db, false)
	require.NoError(t, err)

	assert.Equal(t, int64(1), stats.ChannelUsers)
	assert.Equal(t, int64(1), countRows(t, db, "CHANNEL_USER"))
}

func TestCompact_UserDedup(t *testing.T) {
	db := testDB(t)

	insertSession(t, db, 1)
	insertChunk(t, db, 1, 1, 0)
	insertChunk(t, db, 2, 1, 0)
	insertUser(t, db, "U1", 1)
	insertUser(t, db, "U1", 2)

	stats, err := compact(context.Background(), db, false)
	require.NoError(t, err)

	assert.Equal(t, int64(1), stats.Users)
	assert.Equal(t, int64(1), countRows(t, db, "S_USER"))
}

func TestCompact_ChannelDedup(t *testing.T) {
	db := testDB(t)

	insertSession(t, db, 1)
	insertChunk(t, db, 1, 1, 0)
	insertChunk(t, db, 2, 1, 0)
	insertChannel(t, db, "C1", 1)
	insertChannel(t, db, "C1", 2)

	stats, err := compact(context.Background(), db, false)
	require.NoError(t, err)

	assert.Equal(t, int64(1), stats.Channels)
	assert.Equal(t, int64(1), countRows(t, db, "CHANNEL"))
}

func TestCompact_FileDedup(t *testing.T) {
	db := testDB(t)

	insertSession(t, db, 1)
	insertChunk(t, db, 1, 1, 0)
	insertChunk(t, db, 2, 1, 0)
	insertFile(t, db, "F1", 1, "C1")
	insertFile(t, db, "F1", 2, "C1")

	stats, err := compact(context.Background(), db, false)
	require.NoError(t, err)

	assert.Equal(t, int64(1), stats.Files)
	assert.Equal(t, int64(1), countRows(t, db, "FILE"))
}

func TestCompact_OrphanChunkCleanup(t *testing.T) {
	db := testDB(t)

	insertSession(t, db, 1)
	insertChunk(t, db, 1, 1, 0)
	insertChunk(t, db, 2, 1, 0) // chunk 2 has no data — it's orphaned.
	insertMessage(t, db, 100, 1, "C1", "1.0")

	stats, err := compact(context.Background(), db, false)
	require.NoError(t, err)

	assert.Equal(t, int64(1), stats.Chunks, "orphaned chunk 2 should be removed")
	assert.Equal(t, int64(1), countRows(t, db, "CHUNK"))
}

func TestCompact_OrphanSessionCleanup(t *testing.T) {
	db := testDB(t)

	insertSession(t, db, 1)
	insertSession(t, db, 2) // session 2 has no chunks
	insertChunk(t, db, 1, 1, 0)
	insertMessage(t, db, 100, 1, "C1", "1.0")

	stats, err := compact(context.Background(), db, false)
	require.NoError(t, err)

	assert.Equal(t, int64(1), stats.Sessions, "orphaned session should be removed")
	assert.Equal(t, int64(1), countRows(t, db, "SESSION"))
}

func TestCompact_DryRunAccuracy(t *testing.T) {
	db := testDB(t)

	// Setup: two chunks with duplicate data only (no pre-existing orphans).
	// This way dry-run and actual counts for entity dedup match exactly.
	// Orphan counts may differ because dry-run sees the pre-dedup state,
	// so we only compare entity dedup counts here.
	insertSession(t, db, 1)
	insertChunk(t, db, 1, 1, 0)
	insertChunk(t, db, 2, 1, 0)
	insertMessage(t, db, 100, 1, "C1", "1.0")
	insertMessage(t, db, 100, 2, "C1", "1.0")
	insertChannel(t, db, "C1", 1)
	insertChannel(t, db, "C1", 2)
	insertUser(t, db, "U1", 1)
	insertUser(t, db, "U1", 2)

	// Dry run.
	dryStats, err := compact(context.Background(), db, true)
	require.NoError(t, err)

	// Verify nothing changed.
	assert.Equal(t, int64(2), countRows(t, db, "MESSAGE"), "dry run should not delete messages")
	assert.Equal(t, int64(2), countRows(t, db, "CHANNEL"), "dry run should not delete channels")
	assert.Equal(t, int64(2), countRows(t, db, "S_USER"), "dry run should not delete users")
	assert.Equal(t, int64(2), countRows(t, db, "CHUNK"), "dry run should not delete chunks")
	assert.Equal(t, int64(1), countRows(t, db, "SESSION"), "dry run should not delete sessions")

	// Actual compact.
	actualStats, err := compact(context.Background(), db, false)
	require.NoError(t, err)

	// Entity dedup counts should match between dry-run and actual.
	assert.Equal(t, dryStats.Messages, actualStats.Messages, "message count mismatch")
	assert.Equal(t, dryStats.Channels, actualStats.Channels, "channel count mismatch")
	assert.Equal(t, dryStats.Users, actualStats.Users, "user count mismatch")
}

func TestCompact_DryRunNoChanges(t *testing.T) {
	db := testDB(t)

	insertSession(t, db, 1)
	insertChunk(t, db, 1, 1, 0)
	insertMessage(t, db, 100, 1, "C1", "1.0")

	stats, err := compact(context.Background(), db, true)
	require.NoError(t, err)

	assert.Equal(t, int64(0), stats.total(), "dry run on clean DB should report zero")
	// Verify DB unchanged.
	assert.Equal(t, int64(1), countRows(t, db, "MESSAGE"))
	assert.Equal(t, int64(1), countRows(t, db, "CHUNK"))
	assert.Equal(t, int64(1), countRows(t, db, "SESSION"))
}

func TestCompact_WorkspaceDedup(t *testing.T) {
	db := testDB(t)

	insertSession(t, db, 1)
	insertChunk(t, db, 1, 1, 0)
	insertChunk(t, db, 2, 1, 0)
	insertWorkspace(t, db, 1, "T1")
	insertWorkspace(t, db, 2, "T1")

	stats, err := compact(context.Background(), db, false)
	require.NoError(t, err)

	assert.Equal(t, int64(1), stats.Workspaces)
	assert.Equal(t, int64(1), countRows(t, db, "WORKSPACE"))
}
