package database

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/osean-man/pinner/internal/hlc"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "commands.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func mustAdd(t *testing.T, s *Store, cmd string) Pin {
	t.Helper()
	p, err := s.Add(NewPin{Command: cmd})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// createV0 builds a database exactly as the original pinner (schema version 0) did.
func createV0(t *testing.T, path string, commands ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`
    CREATE TABLE IF NOT EXISTS pins (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        command TEXT NOT NULL,
        timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
    );
    `)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range commands {
		if _, err := db.Exec("INSERT INTO pins(command, timestamp) VALUES (?, '2024-03-01 10:00:00')", c); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigratesOriginalSchemaWithoutLosingPins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.db")
	createV0(t, path, "ls -la", "kubectl get pods -o go-template='{{range .items}}{{.metadata.name}}{{end}}'", "echo hi")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	pins, err := s.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 3 {
		t.Fatalf("got %d pins after migration, want 3", len(pins))
	}
	seen := map[string]bool{}
	for i, p := range pins {
		if p.ID != int64(i+1) {
			t.Errorf("pin %d keeps id %d, want %d", i, p.ID, i+1)
		}
		if _, err := uuid.Parse(p.UUID); err != nil {
			t.Errorf("pin %d has invalid uuid %q", p.ID, p.UUID)
		}
		if seen[p.UUID] {
			t.Errorf("duplicate uuid %q", p.UUID)
		}
		seen[p.UUID] = true
		if _, err := hlc.Parse(p.HLC); err != nil {
			t.Errorf("pin %d has invalid hlc: %v", p.ID, err)
		}
		if want := time.Date(2024, 3, 1, 10, 0, 0, 0, time.UTC); !p.CreatedAt.Equal(want) {
			t.Errorf("created at %v, want %v", p.CreatedAt, want)
		}
		if !p.Dirty || p.Synced {
			t.Errorf("migrated pin should be dirty and unsynced: %+v", p)
		}
	}
	if pins[1].Command != "kubectl get pods -o go-template='{{range .items}}{{.metadata.name}}{{end}}'" {
		t.Errorf("command changed during migration: %q", pins[1].Command)
	}

	// The pre-migration backup is a readable original-schema database.
	bak, err := sql.Open("sqlite", path+".bak")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bak.Close() }()
	var n int
	if err := bak.QueryRow("SELECT count(*) FROM pins").Scan(&n); err != nil || n != 3 {
		t.Fatalf("backup has %d pins (err %v), want 3", n, err)
	}
}

func TestReopenIsIdempotentAndKeepsClockMonotonic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(48 * time.Hour)
	s.SetNow(func() time.Time { return future })
	first := mustAdd(t, s, "echo one")
	device, _ := s.DeviceID()
	_ = s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	if d, _ := s2.DeviceID(); d != device {
		t.Fatalf("device id changed across reopen: %q vs %q", d, device)
	}
	// Even though the wall clock is now "earlier", new edits must sort after old ones.
	second := mustAdd(t, s2, "echo two")
	if second.HLC <= first.HLC {
		t.Fatalf("hlc went backwards across reopen: %s then %s", first.HLC, second.HLC)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Errorf("a fresh database should not produce a backup")
	}
}

func TestDatabaseFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("database mode %o, want 600", perm)
	}
}

func TestAddRejectsDuplicatesAndRevivesRemoved(t *testing.T) {
	s, _ := openTemp(t)
	p := mustAdd(t, s, "  ls -la  ")
	if p.Command != "ls -la" {
		t.Fatalf("command not trimmed: %q", p.Command)
	}
	if _, err := s.Add(NewPin{Command: "ls -la"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("got %v, want ErrDuplicate", err)
	}
	if _, err := s.Add(NewPin{Command: "   "}); !errors.Is(err, ErrEmpty) {
		t.Fatalf("got %v, want ErrEmpty", err)
	}

	if err := s.Remove(p.ID); err != nil {
		t.Fatal(err)
	}
	revived, err := s.Add(NewPin{Command: "ls -la", Label: "list"})
	if err != nil {
		t.Fatal(err)
	}
	if revived.UUID != p.UUID {
		t.Errorf("revive should keep the sync identity: %q vs %q", revived.UUID, p.UUID)
	}
	if revived.HLC <= p.HLC || revived.Label != "list" || revived.Deleted {
		t.Errorf("revived pin not updated: %+v", revived)
	}
}

func TestRemoveLeavesTombstoneHiddenFromList(t *testing.T) {
	s, _ := openTemp(t)
	a := mustAdd(t, s, "echo a")
	mustAdd(t, s, "echo b")
	if err := s.MarkPushed(a.UUID, a.HLC); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(a.ID); err != nil {
		t.Fatal(err)
	}
	pins, _ := s.List("")
	if len(pins) != 1 || pins[0].Command != "echo b" {
		t.Fatalf("list should only show echo b, got %+v", pins)
	}
	if _, err := s.Get(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed pin should not be gettable: %v", err)
	}
	tomb, err := s.GetByUUID(a.UUID)
	if err != nil || !tomb.Deleted || !tomb.Dirty {
		t.Fatalf("expected a dirty tombstone, got %+v (%v)", tomb, err)
	}
	if err := s.Remove(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removing twice: got %v, want ErrNotFound", err)
	}
}

func TestUpdate(t *testing.T) {
	s, _ := openTemp(t)
	a := mustAdd(t, s, "echo a")
	mustAdd(t, s, "echo b")

	dup := "echo b"
	if _, err := s.Update(a.ID, PinUpdate{Command: &dup}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("got %v, want ErrDuplicate", err)
	}
	cmd, label, tags, sens := "echo A", "  shout ", []string{"Demo", "demo", " x "}, true
	u, err := s.Update(a.ID, PinUpdate{Command: &cmd, Label: &label, Tags: &tags, Sensitive: &sens})
	if err != nil {
		t.Fatal(err)
	}
	if u.Command != "echo A" || u.Label != "shout" || !u.Sensitive || len(u.Tags) != 2 || u.Tags[0] != "demo" || u.Tags[1] != "x" {
		t.Fatalf("unexpected update result: %+v", u)
	}
	if u.HLC <= a.HLC || !u.Dirty {
		t.Fatalf("update must advance hlc and mark dirty: %+v", u)
	}
}

func TestListFiltersByTag(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.Add(NewPin{Command: "kubectl get pods", Tags: []string{"k8s", "prod"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(NewPin{Command: "ls", Tags: []string{"fs"}}); err != nil {
		t.Fatal(err)
	}
	pins, _ := s.List("K8S")
	if len(pins) != 1 || pins[0].Command != "kubectl get pods" {
		t.Fatalf("tag filter returned %+v", pins)
	}
}

func TestRecordUseOrdersByFrecencyWithoutCreatingSyncChurn(t *testing.T) {
	s, _ := openTemp(t)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	s.SetNow(func() time.Time { return now })
	a := mustAdd(t, s, "echo a")
	b := mustAdd(t, s, "echo b")
	c := mustAdd(t, s, "echo c")
	for _, p := range []Pin{a, b, c} {
		if err := s.MarkPushed(p.UUID, p.HLC); err != nil {
			t.Fatal(err)
		}
	}

	// c used once just now, b used 3 times a month ago, a never used.
	s.SetNow(func() time.Time { return now.Add(-30 * 24 * time.Hour) })
	for i := 0; i < 3; i++ {
		if err := s.RecordUse(b.ID); err != nil {
			t.Fatal(err)
		}
	}
	s.SetNow(func() time.Time { return now })
	if err := s.RecordUse(c.ID); err != nil {
		t.Fatal(err)
	}

	pins, _ := s.List("")
	got := []string{pins[0].Command, pins[1].Command, pins[2].Command}
	want := []string{"echo c", "echo b", "echo a"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
	dirty, _ := s.Dirty()
	if len(dirty) != 0 {
		t.Fatalf("recording a use must not mark pins dirty, got %d dirty", len(dirty))
	}
	if after, _ := s.Get(c.ID); after.HLC != c.HLC {
		t.Fatalf("recording a use must not advance the hlc")
	}
}

func TestFrecencyHalvesWeekly(t *testing.T) {
	now := time.Now()
	p := Pin{UseCount: 8, LastUsed: now.Add(-14 * 24 * time.Hour)}
	if got := Frecency(p, now); got < 1.99 || got > 2.01 {
		t.Fatalf("8 uses two weeks ago scored %v, want ~2", got)
	}
}

func TestMarkPushedDoesNotClearNewerEdits(t *testing.T) {
	s, _ := openTemp(t)
	p := mustAdd(t, s, "echo a")
	pushed := p.HLC
	label := "edited during push"
	if _, err := s.Update(p.ID, PinUpdate{Label: &label}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPushed(p.UUID, pushed); err != nil {
		t.Fatal(err)
	}
	cur, _ := s.Get(p.ID)
	if !cur.Dirty {
		t.Fatal("an edit made after the push was read must stay dirty")
	}
}

func remoteTS(wall int64, node string) string {
	return hlc.Timestamp{Wall: wall, Node: node}.String()
}

func TestApplyRemoteLastWriterWins(t *testing.T) {
	s, _ := openTemp(t)
	p := mustAdd(t, s, "echo local")

	older := p.ToRecord()
	older.Command = "echo stale"
	older.HLC = remoteTS(1, "other")
	if changed, err := s.ApplyRemote(older); err != nil || changed {
		t.Fatalf("older remote version must be ignored (changed=%v err=%v)", changed, err)
	}

	newer := p.ToRecord()
	newer.Command = "echo remote"
	newer.HLC = remoteTS(time.Now().Add(time.Hour).UnixMilli(), "other")
	if changed, err := s.ApplyRemote(newer); err != nil || !changed {
		t.Fatalf("newer remote version must apply (changed=%v err=%v)", changed, err)
	}
	cur, _ := s.Get(p.ID)
	if cur.Command != "echo remote" || cur.Dirty || !cur.Synced {
		t.Fatalf("unexpected state after remote apply: %+v", cur)
	}

	// The local clock observed the remote edit, so the next local edit wins against it.
	label := "after"
	edited, err := s.Update(p.ID, PinUpdate{Label: &label})
	if err != nil {
		t.Fatal(err)
	}
	if edited.HLC <= newer.HLC {
		t.Fatalf("local edit %s should sort after observed remote %s", edited.HLC, newer.HLC)
	}

	tomb := edited.ToRecord()
	tomb.Deleted = true
	tomb.HLC = remoteTS(time.Now().Add(2*time.Hour).UnixMilli(), "other")
	if _, err := s.ApplyRemote(tomb); err != nil {
		t.Fatal(err)
	}
	if pins, _ := s.List(""); len(pins) != 0 {
		t.Fatalf("remote delete should hide the pin, got %+v", pins)
	}
}

func TestApplyRemoteIgnoresLocalOnlyPins(t *testing.T) {
	s, _ := openTemp(t)
	p, err := s.Add(NewPin{Command: "cat ~/.secret", Local: true})
	if err != nil {
		t.Fatal(err)
	}
	r := p.ToRecord()
	r.Command = "rm -rf ~"
	r.HLC = remoteTS(time.Now().Add(time.Hour).UnixMilli(), "other")
	if changed, _ := s.ApplyRemote(r); changed {
		t.Fatal("remote record must not overwrite a local-only pin")
	}
	cur, _ := s.Get(p.ID)
	if cur.Command != "cat ~/.secret" {
		t.Fatalf("local-only pin changed to %q", cur.Command)
	}
}

func TestApplyRemoteAdoptsIdenticalUnsyncedPin(t *testing.T) {
	s, _ := openTemp(t)
	mine := mustAdd(t, s, "git status")

	theirs := Record{UUID: uuid.NewString(), Command: "git status", Label: "gs", CreatedAt: 1, HLC: remoteTS(time.Now().Add(time.Hour).UnixMilli(), "other")}
	if _, err := s.ApplyRemote(theirs); err != nil {
		t.Fatal(err)
	}
	pins, _ := s.List("")
	if len(pins) != 1 {
		t.Fatalf("joining should not duplicate identical pins, got %d", len(pins))
	}
	if pins[0].ID != mine.ID || pins[0].UUID != theirs.UUID || pins[0].Label != "gs" {
		t.Fatalf("pin should adopt the remote identity and newer fields: %+v", pins[0])
	}
}

func TestApplyRemoteInsertsNewAndRejectsGarbage(t *testing.T) {
	s, _ := openTemp(t)
	r := Record{UUID: uuid.NewString(), Command: "uptime", Tags: []string{"ops"}, Sensitive: true, CreatedAt: 1700000000, HLC: remoteTS(5, "other")}
	if changed, err := s.ApplyRemote(r); err != nil || !changed {
		t.Fatalf("insert failed: changed=%v err=%v", changed, err)
	}
	got, err := s.GetByUUID(r.UUID)
	if err != nil || got.Command != "uptime" || !got.Sensitive || got.Tags[0] != "ops" || got.CreatedAt.Unix() != 1700000000 || got.Dirty {
		t.Fatalf("unexpected inserted pin %+v (%v)", got, err)
	}
	for _, bad := range []Record{
		{UUID: "not-a-uuid", Command: "x", HLC: remoteTS(5, "o")},
		{UUID: uuid.NewString(), Command: "x", HLC: "garbage"},
		{UUID: uuid.NewString(), Command: "  ", HLC: remoteTS(5, "o")},
	} {
		if _, err := s.ApplyRemote(bad); err == nil {
			t.Errorf("ApplyRemote(%+v) should fail", bad)
		}
	}
}

func TestAdoptionMergesIndependentCopies(t *testing.T) {
	s, _ := openTemp(t)
	// The local copy is newer but has no label; the remote copy is older, labelled and sensitive.
	if _, err := s.Add(NewPin{Command: "psql $DATABASE_URL", Tags: []string{"db"}}); err != nil {
		t.Fatal(err)
	}
	remote := Record{UUID: uuid.NewString(), Command: "psql $DATABASE_URL", Label: "prod db", Tags: []string{"prod"}, Sensitive: true, CreatedAt: 1, HLC: remoteTS(1, "other")}
	if _, err := s.ApplyRemote(remote); err != nil {
		t.Fatal(err)
	}
	pins, _ := s.List("")
	if len(pins) != 1 {
		t.Fatalf("got %d pins", len(pins))
	}
	p := pins[0]
	if p.UUID != remote.UUID || p.Label != "prod db" || !p.Sensitive || joinTags(p.Tags) != "db,prod" {
		t.Fatalf("merge result %+v", p)
	}
	if !p.Dirty || p.HLC <= remote.HLC {
		t.Fatalf("a merge that differs from the server must be pushed with a newer version: %+v", p)
	}
}
