//go:build linux

package workflowrecovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"
)

// sqlitePageGeometry is the engine-reported geometry of one connection. Only
// PageSize and AutoVacuum are stored in the file; MaxPageCount is per connection.
type sqlitePageGeometry struct {
	PageSize     int64
	AutoVacuum   int64
	MaxPageCount int64
	PageCount    int64
}

func readSQLitePageGeometry(t *testing.T, db *sql.DB) sqlitePageGeometry {
	t.Helper()
	ctx := context.Background()
	var geometry sqlitePageGeometry
	if err := db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&geometry.PageSize); err != nil {
		t.Fatalf("read page_size: %v", err)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&geometry.AutoVacuum); err != nil {
		t.Fatalf("read auto_vacuum: %v", err)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA max_page_count").Scan(&geometry.MaxPageCount); err != nil {
		t.Fatalf("read max_page_count: %v", err)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&geometry.PageCount); err != nil {
		t.Fatalf("read page_count: %v", err)
	}
	return geometry
}

func requireSQLitePageGeometry(t *testing.T, label string, got, want sqlitePageGeometry) {
	t.Helper()
	if got.PageSize != want.PageSize || got.AutoVacuum != want.AutoVacuum || got.MaxPageCount != want.MaxPageCount {
		t.Fatalf("%s geometry = page_size %d auto_vacuum %d max_page_count %d, want %d/%d/%d", label,
			got.PageSize, got.AutoVacuum, got.MaxPageCount, want.PageSize, want.AutoVacuum, want.MaxPageCount)
	}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func openRWSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	dsnURL := url.URL{Scheme: "file", Path: path, RawQuery: url.Values{"mode": {"rw"}}.Encode()}
	db, err := sql.Open("sqlite", dsnURL.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return db
}

// createClosedOwner creates and closes a fresh SQLite owner file.
func createClosedOwner(t *testing.T) (*testTrustedRoot, string) {
	t.Helper()
	root := newTestTrustedRoot()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "workflow-owner.sqlite")
	owner, err := CreateFreshSQLiteOwner(path, root)
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	return root, path
}

// createClosedProtectedRoot creates and closes a fresh protected root file.
func createClosedProtectedRoot(t *testing.T, workflow string) (string, string, *LinuxHostBoundary) {
	t.Helper()
	rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
	boundary, created := createProtectedRoot(t, rootPath, ownerStoreRoot, workflow)
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}
	return rootPath, ownerStoreRoot, boundary
}

// rewriteSQLiteGeometry produces an existing, schema-valid file whose geometry
// differs from the contract. VACUUM is the engine path that rewrites a database
// under a new page size or auto-vacuum mode; WAL is restored afterwards.
func rewriteSQLiteGeometry(t *testing.T, path, geometryPragma string, applied func(sqlitePageGeometry) bool) {
	t.Helper()
	db := openRWSQLite(t, path)
	defer db.Close()
	ctx := context.Background()
	for _, statement := range []string{"PRAGMA journal_mode=DELETE", geometryPragma, "VACUUM", "PRAGMA journal_mode=WAL"} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if got := readSQLitePageGeometry(t, db); !applied(got) {
		t.Fatalf("VACUUM did not apply %s: geometry=%+v", geometryPragma, got)
	}
}

func TestSQLitePageGeometryIsFixedOnFreshAndReopen(t *testing.T) {
	owner, root, _, _, _ := newWorkflowHarness(t, "workflow-geometry", testLimits())
	want := sqlitePageGeometry{PageSize: sqlitePageSize, AutoVacuum: 0, MaxPageCount: sqliteOwnerMaxPageCount}
	requireSQLitePageGeometry(t, "fresh owner", readSQLitePageGeometry(t, owner.db), want)
	path := owner.path
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}

	// The writer ceiling lives on each connection, not in the file. The
	// read-only settled view must keep SQLite's default and set no limit.
	readOnly, err := openSettledOwnerDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := readSQLitePageGeometry(t, readOnly); got.MaxPageCount == sqliteOwnerMaxPageCount || got.PageSize != sqlitePageSize {
		t.Fatalf("read-only view geometry = %+v; want the file page size and no writer ceiling", got)
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenExistingSQLiteOwner(path, root)
	if err != nil {
		t.Fatalf("reopen fresh owner: %v", err)
	}
	defer reopened.Close()
	requireSQLitePageGeometry(t, "reopened owner", readSQLitePageGeometry(t, reopened.db), want)

	// Expiring the pooled connection forces database/sql to open a new one. The
	// replacement must carry the ceiling because it is applied from the DSN.
	replaced := reopened.db.Stats().MaxLifetimeClosed
	reopened.db.SetConnMaxLifetime(time.Nanosecond)
	requireSQLitePageGeometry(t, "pooled replacement", readSQLitePageGeometry(t, reopened.db), want)
	reopened.db.SetConnMaxLifetime(0)
	if reopened.db.Stats().MaxLifetimeClosed <= replaced {
		t.Fatal("pooled connection was not replaced; the reconnect path was not exercised")
	}

	rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, "workflow-root-geometry", &processExitDrainVerifier{})
	rootWant := sqlitePageGeometry{PageSize: sqlitePageSize, AutoVacuum: 0, MaxPageCount: sqliteProtectedRootMaxPageCount}
	created, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("create protected root: %v", err)
	}
	requireSQLitePageGeometry(t, "fresh protected root", readSQLitePageGeometry(t, created.db), rootWant)
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedRoot, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("reopen protected root: %v", err)
	}
	defer reopenedRoot.Close()
	requireSQLitePageGeometry(t, "reopened protected root", readSQLitePageGeometry(t, reopenedRoot.db), rootWant)
}

func TestSQLiteOwnerRefusesIncompatiblePageGeometryWithoutModifyingFile(t *testing.T) {
	cases := []struct {
		name    string
		pragma  string
		applied func(sqlitePageGeometry) bool
	}{
		{name: "page-size-8192", pragma: "PRAGMA page_size=8192",
			applied: func(g sqlitePageGeometry) bool { return g.PageSize == 8192 }},
		{name: "auto-vacuum-full", pragma: "PRAGMA auto_vacuum=FULL",
			applied: func(g sqlitePageGeometry) bool { return g.AutoVacuum == 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, path := createClosedOwner(t)
			rewriteSQLiteGeometry(t, path, tc.pragma, tc.applied)
			before := fileSHA256(t, path)
			if _, err := OpenExistingSQLiteOwner(path, root); !errors.Is(err, ErrOwnerStore) ||
				!strings.Contains(err.Error(), "page geometry differs") {
				t.Fatalf("incompatible owner open = %v, want ErrOwnerStore geometry refusal", err)
			}
			if after := fileSHA256(t, path); after != before {
				t.Fatal("refused incompatible owner open modified the database file")
			}
		})
	}
}

func TestProtectedRootRefusesIncompatiblePageGeometryWithoutModifyingFile(t *testing.T) {
	rootPath, ownerStoreRoot, boundary := createClosedProtectedRoot(t, "workflow-root-mismatch")
	rewriteSQLiteGeometry(t, rootPath, "PRAGMA page_size=8192",
		func(g sqlitePageGeometry) bool { return g.PageSize == 8192 })
	before := fileSHA256(t, rootPath)
	if _, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot, boundary); !errors.Is(err, ErrOwnerStore) ||
		!strings.Contains(err.Error(), "page geometry differs") {
		t.Fatalf("incompatible root open = %v, want ErrOwnerStore geometry refusal", err)
	}
	if after := fileSHA256(t, rootPath); after != before {
		t.Fatal("refused incompatible root open modified the root database file")
	}
}

const sqliteRetainedWALHelperEnv = "TBOUND_SQLITE_RETAINED_WAL_HELPER"

// TestSQLiteRetainedWALGeometryHelper runs only in a fresh process. It rewrites
// the file named by the environment to 8192-byte pages, commits rows that stay
// in the WAL, and returns without Close. Process exit leaves -wal and -shm
// behind, which is the state of a writer that crashed with an incompatible
// geometry.
func TestSQLiteRetainedWALGeometryHelper(t *testing.T) {
	path := os.Getenv(sqliteRetainedWALHelperEnv)
	if path == "" {
		return
	}
	db := openRWSQLite(t, path)
	ctx := context.Background()
	for _, statement := range []string{
		"PRAGMA journal_mode=DELETE",
		"PRAGMA page_size=8192",
		"VACUUM",
		"PRAGMA journal_mode=WAL",
		"PRAGMA wal_autocheckpoint=0",
		"CREATE TABLE retained_wal_probe(id INTEGER PRIMARY KEY, body BLOB NOT NULL)",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	for id := 1; id <= 64; id++ {
		if _, err := db.ExecContext(ctx, "INSERT INTO retained_wal_probe(id, body) VALUES(?, ?)",
			id, bytes.Repeat([]byte{byte(id)}, 4000)); err != nil {
			t.Fatal(err)
		}
	}
	// No Close: returning leaves the committed frames in -wal.
}

// runRetainedWALGeometryHelper creates the retained-WAL state in a child
// process and checks its precondition: committed frames in -wal and an -shm
// index, both left by a process that never closed the database.
func runRetainedWALGeometryHelper(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSQLiteRetainedWALGeometryHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), sqliteRetainedWALHelperEnv+"="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("retained-WAL helper process failed: %v\n%s", err, out)
	}
	info, err := os.Stat(path + "-wal")
	if err != nil || info.Size() == 0 {
		t.Fatalf("helper left no retained WAL frames: stat error=%v", err)
	}
	if _, err := os.Stat(path + "-shm"); err != nil {
		t.Fatalf("helper left no -shm index: %v", err)
	}
}

// mainAndSidecarSHA256 digests the database file and one sidecar, -wal or
// -journal. An absent sidecar is reported as "absent", so a refusal that
// checkpoints, rolls back, or deletes it changes the digest even when the
// logical rows are unchanged.
func mainAndSidecarSHA256(t *testing.T, path, suffix string) (string, string) {
	t.Helper()
	sidecarDigest := "absent"
	if _, err := os.Stat(path + suffix); err == nil {
		sidecarDigest = fileSHA256(t, path+suffix)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return fileSHA256(t, path), sidecarDigest
}

// TestSQLiteOwnerRefusesRetainedWALGeometryWithoutModifyingFiles covers an
// incompatible owner left with a retained WAL by a process that never closed
// it. The refusal must return ErrOwnerStore and leave both the main database
// and the WAL byte-identical. A writable close would checkpoint and delete the
// WAL, so the geometry must be checked through a read-only connection first.
func TestSQLiteOwnerRefusesRetainedWALGeometryWithoutModifyingFiles(t *testing.T) {
	root, path := createClosedOwner(t)
	runRetainedWALGeometryHelper(t, path)
	mainBefore, walBefore := mainAndSidecarSHA256(t, path, "-wal")
	owner, err := OpenExistingSQLiteOwner(path, root)
	if err == nil {
		_ = owner.Close()
		t.Fatal("incompatible owner with a retained WAL was admitted")
	}
	if !errors.Is(err, ErrOwnerStore) || !strings.Contains(err.Error(), "page geometry differs") {
		t.Fatalf("retained-WAL owner open = %v, want ErrOwnerStore geometry refusal", err)
	}
	if mainAfter, walAfter := mainAndSidecarSHA256(t, path, "-wal"); mainAfter != mainBefore || walAfter != walBefore {
		t.Fatalf("refused retained-WAL owner open changed the file: main %s -> %s, wal %s -> %s",
			mainBefore, mainAfter, walBefore, walAfter)
	}
}

// TestProtectedRootRefusesRetainedWALGeometryWithoutModifyingFiles is the
// protected-root form of the owner case above.
func TestProtectedRootRefusesRetainedWALGeometryWithoutModifyingFiles(t *testing.T) {
	rootPath, ownerStoreRoot, boundary := createClosedProtectedRoot(t, "workflow-root-retained-wal")
	runRetainedWALGeometryHelper(t, rootPath)
	mainBefore, walBefore := mainAndSidecarSHA256(t, rootPath, "-wal")
	root, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err == nil {
		_ = root.Close()
		t.Fatal("incompatible protected root with a retained WAL was admitted")
	}
	if !errors.Is(err, ErrOwnerStore) || !strings.Contains(err.Error(), "page geometry differs") {
		t.Fatalf("retained-WAL root open = %v, want ErrOwnerStore geometry refusal", err)
	}
	if mainAfter, walAfter := mainAndSidecarSHA256(t, rootPath, "-wal"); mainAfter != mainBefore || walAfter != walBefore {
		t.Fatalf("refused retained-WAL root open changed the file: main %s -> %s, wal %s -> %s",
			mainBefore, mainAfter, walBefore, walAfter)
	}
}

const sqliteHotJournalHelperEnv = "TBOUND_SQLITE_HOT_JOURNAL_HELPER"

// TestSQLiteHotJournalHelper runs only in a fresh process. It switches the file
// named by the environment to rollback-journal mode, fills it to about 4,000
// pages, and starts an UPDATE of every row with cache_size=2 and cache_spill ON,
// so dirty pages reach the main file before COMMIT. Returning without Commit or
// Close leaves a hot -journal that only a writable connection can roll back.
// synchronous=OFF only skips fsync; the fixture models process exit, not power loss.
func TestSQLiteHotJournalHelper(t *testing.T) {
	path := os.Getenv(sqliteHotJournalHelperEnv)
	if path == "" {
		return
	}
	db := openRWSQLite(t, path)
	ctx := context.Background()
	for _, statement := range []string{
		"PRAGMA journal_mode=DELETE",
		"PRAGMA synchronous=OFF",
		"CREATE TABLE hot_journal_probe(id INTEGER PRIMARY KEY, body BLOB NOT NULL)",
		"WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<4000) " +
			"INSERT INTO hot_journal_probe(id, body) SELECT i, zeroblob(3000) FROM n",
		"PRAGMA cache_size=2",
		"PRAGMA cache_spill=ON",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE hot_journal_probe SET body=?", bytes.Repeat([]byte{0xa5}, 3000)); err != nil {
		t.Fatal(err)
	}
	// No Commit and no Close: returning leaves the hot -journal.
}

// runHotJournalHelper leaves a hot rollback journal beside path in a child
// process. It checks the journal exists, so a refusal cannot be blamed on a
// file that never had one.
func runHotJournalHelper(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSQLiteHotJournalHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), sqliteHotJournalHelperEnv+"="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hot-journal helper process failed: %v\n%s", err, out)
	}
	journal, err := os.Stat(path + "-journal")
	if err != nil || journal.Size() == 0 {
		t.Fatalf("helper left no hot journal: stat error=%v", err)
	}
	database, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("hot journal left beside %d-page main file (%d bytes), journal %d bytes",
		database.Size()/sqlitePageSize, database.Size(), journal.Size())
}

// TestSQLiteOwnerRefusesHotJournalWithoutModifyingFiles covers an owner whose
// rollback journal was left hot by a process that exited mid-UPDATE. A
// read-only connection cannot roll the journal back, so the refusal must come
// from the read-only precheck. A writable open would roll back and delete the
// journal before the later WAL-profile check refuses the file.
func TestSQLiteOwnerRefusesHotJournalWithoutModifyingFiles(t *testing.T) {
	root, path := createClosedOwner(t)
	runHotJournalHelper(t, path)
	mainBefore, journalBefore := mainAndSidecarSHA256(t, path, "-journal")
	owner, err := OpenExistingSQLiteOwner(path, root)
	if err == nil {
		_ = owner.Close()
		t.Fatal("owner with a hot journal was admitted")
	}
	t.Logf("refusal: %v", err)
	if mainAfter, journalAfter := mainAndSidecarSHA256(t, path, "-journal"); mainAfter != mainBefore || journalAfter != journalBefore {
		t.Fatalf("refused hot-journal owner open changed the file: main %s -> %s, journal %s -> %s",
			mainBefore, mainAfter, journalBefore, journalAfter)
	}
	if !errors.Is(err, ErrOwnerStore) || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("hot-journal owner open = %v, want ErrOwnerStore refusal from the read-only precheck", err)
	}
}

// TestProtectedRootRefusesHotJournalWithoutModifyingFiles is the protected-root
// form of the owner case above. The root is created first so its lock file
// exists before the child runs.
func TestProtectedRootRefusesHotJournalWithoutModifyingFiles(t *testing.T) {
	rootPath, ownerStoreRoot, boundary := createClosedProtectedRoot(t, "workflow-root-hot-journal")
	runHotJournalHelper(t, rootPath)
	mainBefore, journalBefore := mainAndSidecarSHA256(t, rootPath, "-journal")
	root, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err == nil {
		_ = root.Close()
		t.Fatal("protected root with a hot journal was admitted")
	}
	t.Logf("refusal: %v", err)
	if mainAfter, journalAfter := mainAndSidecarSHA256(t, rootPath, "-journal"); mainAfter != mainBefore || journalAfter != journalBefore {
		t.Fatalf("refused hot-journal root open changed the file: main %s -> %s, journal %s -> %s",
			mainBefore, mainAfter, journalBefore, journalAfter)
	}
	if !errors.Is(err, ErrOwnerStore) || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("hot-journal root open = %v, want ErrOwnerStore refusal from the read-only precheck", err)
	}
}

const sqliteCompatibleWALHelperEnv = "TBOUND_SQLITE_COMPATIBLE_WAL_HELPER"

// TestSQLiteCompatibleRetainedWALHelper runs only in a fresh process. It commits
// rows into the WAL at the owner's own geometry and returns without Close, so
// -wal keeps the committed frames and -shm keeps the index.
func TestSQLiteCompatibleRetainedWALHelper(t *testing.T) {
	path := os.Getenv(sqliteCompatibleWALHelperEnv)
	if path == "" {
		return
	}
	db := openRWSQLite(t, path)
	ctx := context.Background()
	for _, statement := range []string{
		"PRAGMA wal_autocheckpoint=0",
		"CREATE TABLE retained_wal_probe(id INTEGER PRIMARY KEY, body BLOB NOT NULL)",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	for id := 1; id <= 64; id++ {
		if _, err := db.ExecContext(ctx, "INSERT INTO retained_wal_probe(id, body) VALUES(?, ?)",
			id, bytes.Repeat([]byte{byte(id)}, 4000)); err != nil {
			t.Fatal(err)
		}
	}
	// No Close: returning leaves the committed frames in -wal and the index in -shm.
}

// TestSQLiteOwnerRecoversCompatibleRetainedWALWithoutSHM is the compatibility
// control for the read-only precheck. A compatible owner whose retained WAL lost
// its -shm index is recoverable, so the precheck must admit it and the writable
// open must recover every committed frame.
func TestSQLiteOwnerRecoversCompatibleRetainedWALWithoutSHM(t *testing.T) {
	root, path := createClosedOwner(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestSQLiteCompatibleRetainedWALHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), sqliteCompatibleWALHelperEnv+"="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compatible retained-WAL helper process failed: %v\n%s", err, out)
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("helper left no retained WAL frames: stat error=%v", err)
	}
	if err := os.Remove(path + "-shm"); err != nil {
		t.Fatalf("helper left no -shm index to remove: %v", err)
	}
	reopened, err := OpenExistingSQLiteOwner(path, root)
	if err != nil {
		t.Fatalf("compatible owner with a retained WAL and no -shm was refused: %v", err)
	}
	defer reopened.Close()
	var rows int64
	if err := reopened.db.QueryRowContext(context.Background(), "SELECT count(*) FROM retained_wal_probe").Scan(&rows); err != nil {
		t.Fatalf("read recovered WAL rows: %v", err)
	}
	if rows != 64 {
		t.Fatalf("recovered %d retained WAL rows, want 64", rows)
	}
}

func TestSQLiteOwnerRefusesExistingDatabaseBeyondCeiling(t *testing.T) {
	owner, root, _, _, _ := newWorkflowHarness(t, "workflow-existing-ceiling", testLimits())
	path := owner.path
	pages := readSQLitePageGeometry(t, owner.db).PageCount
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	before := fileSHA256(t, path)

	// SQLite clamps a ceiling below the current size to that size instead of
	// failing, so the over-ceiling refusal must be an explicit check.
	if _, err := openExistingSQLiteOwnerWithCeiling(path, root, pages-1); !errors.Is(err, ErrOwnerStore) ||
		!strings.Contains(err.Error(), "beyond") {
		t.Fatalf("over-ceiling owner open = %v, want ErrOwnerStore beyond-ceiling refusal", err)
	}
	if after := fileSHA256(t, path); after != before {
		t.Fatal("over-ceiling refusal modified the owner database file")
	}

	// Exactly the current size is admitted: the store can verify and read, but
	// cannot grow past it.
	atCeiling, err := openExistingSQLiteOwnerWithCeiling(path, root, pages)
	if err != nil {
		t.Fatalf("open at exact ceiling: %v", err)
	}
	defer atCeiling.Close()
	requireSQLitePageGeometry(t, "exact-ceiling owner", readSQLitePageGeometry(t, atCeiling.db),
		sqlitePageGeometry{PageSize: sqlitePageSize, AutoVacuum: 0, MaxPageCount: pages})
}

// TestSQLiteOwnerPageCeilingAdmitsThreeMaxSizeTransitionsAndRefusesFourth
// checks the derivation of sqliteOwnerMaxPageCount against the real engine. A
// fresh owner with a maximum-size checkpoint rewrite plus a maximum-size event
// (checkpoint and proof, 48 MiB logical) is admitted three times, and the
// fourth is refused with SQLITE_FULL with no partial growth.
func TestSQLiteOwnerPageCeilingAdmitsThreeMaxSizeTransitionsAndRefusesFourth(t *testing.T) {
	const workflow = "workflow-max-transitions"
	owner, _, _, _, _ := newWorkflowHarness(t, workflow, testLimits())
	ctx := context.Background()
	maxBlob := bytes.Repeat([]byte("x"), maxCheckpointJSONBytes)
	transition := func(sequence int64) error {
		tx, err := owner.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin max-size transition %d: %v", sequence, err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_recovery_state SET sequence=?,checkpoint_digest=?,checkpoint_json=?
			WHERE workflow_id=?`, sequence, testDigest(fmt.Sprintf("max-state-%d", sequence)), maxBlob, workflow); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_recovery_events(workflow_id,sequence,owner_epoch,expected_digest,
			checkpoint_digest,checkpoint_json,proof_json) VALUES(?,?,1,'',?,?,?)`, workflow, sequence,
			testDigest(fmt.Sprintf("max-event-%d", sequence)), maxBlob, maxBlob); err != nil {
			return err
		}
		return tx.Commit()
	}
	for sequence := int64(2); sequence <= 5; sequence++ {
		before := readSQLitePageGeometry(t, owner.db).PageCount
		err := transition(sequence)
		if sequence < 5 {
			if err != nil {
				t.Fatalf("max-size transition %d refused below the ceiling: %v", sequence, err)
			}
			t.Logf("max-size transition %d admitted: pages %d -> %d of %d", sequence, before,
				readSQLitePageGeometry(t, owner.db).PageCount, sqliteOwnerMaxPageCount)
			continue
		}
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code() != 13 {
			t.Fatalf("fourth max-size transition = %v, want SQLITE_FULL (13)", err)
		}
		if after := readSQLitePageGeometry(t, owner.db).PageCount; after != before {
			t.Fatalf("refused transition changed page count %d -> %d", before, after)
		}
		t.Logf("fourth max-size transition refused with SQLITE_FULL at %d pages", before)
	}
}

// TestSQLiteOwnerCeilingRefusesEffectReservationWithoutGrant reserves an effect
// with the ceiling at the current size. The headroom gate refuses before any row
// is written, so the refusal is a typed allowance refusal, not a compare-and-swap
// conflict or a SQLite fault. It must return no receipt, must leave the ledger
// and durable head at the predecessor, and must not poison the owner.
func TestSQLiteOwnerCeilingRefusesEffectReservationWithoutGrant(t *testing.T) {
	const workflow = "workflow-ceiling-reserve"
	owner, root, ledger, _, _ := newWorkflowHarness(t, workflow, testLimits())
	ctx := context.Background()
	if _, err := ledger.RegisterInitialTranscript(ctx, allowProgress{}, emptyTranscript("conversation-ceiling")); err != nil {
		t.Fatalf("register transcript before the ceiling: %v", err)
	}
	prior, priorDigest := ledger.Snapshot()
	path := owner.path
	pages := readSQLitePageGeometry(t, owner.db).PageCount
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}

	ceilinged, err := openExistingSQLiteOwnerWithCeiling(path, root, pages)
	if err != nil {
		t.Fatalf("reopen at the current ceiling: %v", err)
	}
	defer ceilinged.Close()
	reopenedLedger, err := Open(ctx, ceilinged, OpenOptions{
		WorkflowID: workflow, OwnerEpoch: prior.Pins.OwnerEpoch, RetainedPins: prior.Pins, Limits: prior.Limits,
	})
	if err != nil {
		t.Fatalf("open ledger at the ceiling: %v", err)
	}
	receipt, reserveErr := reopenedLedger.ReserveEffect(ctx, testEffectBinding("effect-at-ceiling"), nil)
	if reserveErr == nil {
		t.Fatalf("reservation succeeded at the page ceiling: receipt=%q", receipt.CheckpointDigest())
	}
	if !errors.Is(reserveErr, ErrExhausted) || errors.Is(reserveErr, ErrCAS) {
		t.Fatalf("ceiling reservation refusal = %v, want typed ErrExhausted without ErrCAS", reserveErr)
	}
	var refusedBySQLite *sqlite.Error
	if errors.As(reserveErr, &refusedBySQLite) {
		t.Fatalf("headroom refusal reached SQLite before any write: %v", reserveErr)
	}
	if receipt.CheckpointDigest() != "" || receipt.WorkflowID() != "" {
		t.Fatalf("refused reservation returned a receipt: workflow=%q digest=%q", receipt.WorkflowID(), receipt.CheckpointDigest())
	}
	if _, ok := receipt.EffectBinding(); ok {
		t.Fatalf("refused reservation returned an effect binding: %+v", receipt)
	}
	if after, afterDigest := reopenedLedger.Snapshot(); afterDigest != priorDigest || !reflect.DeepEqual(after, prior) {
		t.Fatalf("refused reservation changed the in-memory ledger")
	}
	if ceilinged.poisoned {
		t.Fatal("SQLITE_FULL before commit poisoned the owner; the write rolled back cleanly")
	}
	if _, digest, exists, err := ceilinged.LoadCheckpoint(ctx, workflow); err != nil || !exists || digest != priorDigest {
		t.Fatalf("durable head after refusal: digest=%s exists=%v err=%v; want predecessor %s", digest, exists, err, priorDigest)
	}
	// A retry is refused by the same gate. Nothing was granted, so there is no
	// replay of the consumed authority.
	if _, retryErr := reopenedLedger.ReserveEffect(ctx, testEffectBinding("effect-at-ceiling"), nil); !errors.Is(retryErr, ErrExhausted) {
		t.Fatalf("retry at the page ceiling = %v, want typed ErrExhausted", retryErr)
	}
}
