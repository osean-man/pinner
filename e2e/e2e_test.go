// Package e2e builds the real pinner and pinner-server binaries and drives them like a user would.
// Interactive tests run the menu inside an isolated tmux server and are skipped if tmux is missing.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var (
	binDir    string
	pinnerBin string
	serverBin string
	repoRoot  string
)

func TestMain(m *testing.M) {
	if runtime.GOOS == "windows" {
		fmt.Println("skipping e2e tests: they drive a Unix shell")
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "pinner-e2e-bin-")
	if err != nil {
		panic(err)
	}
	binDir = dir
	wd, _ := os.Getwd()
	repoRoot = filepath.Dir(wd)
	pinnerBin = filepath.Join(binDir, "pinner")
	serverBin = filepath.Join(binDir, "pinner-server")
	for out, pkg := range map[string]string{pinnerBin: ".", serverBin: "./cmd/pinner-server"} {
		build := exec.Command("go", "build", "-o", out, pkg)
		build.Dir = repoRoot
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := build.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "building %s: %v\n%s", pkg, err, b)
			os.Exit(1)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(binDir)
	os.Exit(code)
}

// device is one pinner installation with its own config dir.
type device struct {
	t   *testing.T
	dir string
	env []string
}

func newDevice(t *testing.T) *device {
	t.Helper()
	dir := t.TempDir()
	return &device{t: t, dir: dir, env: []string{
		"PINNER_CONFIG_DIR=" + dir,
		"HOME=" + dir, // keep history lookups and anything else away from the real home
		"PATH=" + os.Getenv("PATH"),
		"SHELL=/bin/sh",
		"PINNER_SYNC=",
	}}
}

type result struct {
	stdout, stderr string
	code           int
}

func (d *device) run(stdin string, args ...string) result {
	d.t.Helper()
	cmd := exec.Command(pinnerBin, args...)
	cmd.Env = d.env
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		d.t.Fatal(err)
	}
	return result{out.String(), errb.String(), code}
}

func (d *device) ok(stdin string, args ...string) result {
	d.t.Helper()
	r := d.run(stdin, args...)
	if r.code != 0 {
		d.t.Fatalf("pinner %v exited %d\nstdout: %s\nstderr: %s", args, r.code, r.stdout, r.stderr)
	}
	return r
}

type exported struct {
	Pins []struct {
		Command   string   `json:"command"`
		Label     string   `json:"label"`
		Tags      []string `json:"tags"`
		Sensitive bool     `json:"sensitive"`
		Local     bool     `json:"local"`
	} `json:"pins"`
}

func (d *device) export() exported {
	d.t.Helper()
	var e exported
	if err := json.Unmarshal([]byte(d.ok("", "export").stdout), &e); err != nil {
		d.t.Fatal(err)
	}
	return e
}

func (d *device) commands() []string {
	d.t.Helper()
	var out []string
	for _, p := range d.export().Pins {
		out = append(out, p.Command)
	}
	return out
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
		if m[x]--; m[x] < 0 {
			return false
		}
	}
	return true
}

func TestAddFromArgumentsStdinAndFlags(t *testing.T) {
	d := newDevice(t)
	d.ok("", "add", "ls", "-la") // flags after the command belong to the command
	d.ok("echo piped\n", "add")
	d.ok("for i in 1 2; do\n  echo $i\ndone\n", "add", "-n", "loop")
	d.ok("", "add", "-n", "logs", "-t", "k8s,Prod", "-t", "ops", "--", "kubectl logs -f")

	if r := d.run("", "add", "ls", "-la"); r.code != 1 || !strings.Contains(r.stderr, "already exists") {
		t.Fatalf("duplicate add: %+v", r)
	}
	if r := d.run("", "add", "--last", "x"); r.code == 0 {
		t.Fatal("--last with a command argument should be rejected")
	}

	e := d.export()
	want := []string{"ls -la", "echo piped", "for i in 1 2; do\n  echo $i\ndone", "kubectl logs -f"}
	if !sameSet(d.commands(), want) {
		t.Fatalf("commands %q, want %q", d.commands(), want)
	}
	for _, p := range e.Pins {
		if p.Command == "kubectl logs -f" && (p.Label != "logs" || strings.Join(p.Tags, ",") != "k8s,prod,ops") {
			t.Fatalf("label/tags not stored: %+v", p)
		}
	}
}

func TestVersionReportsTheBuild(t *testing.T) {
	d := newDevice(t)
	out := d.ok("", "--version").stdout
	head, err := exec.Command("git", "-C", repoRoot, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Skip("not a git checkout")
	}
	if !strings.HasPrefix(out, "pinner ") || !strings.Contains(out, "commit "+string(head[:12])) {
		t.Fatalf("--version = %q, want the checkout's commit %s", out, head[:12])
	}
}

func TestAddLastFromHistoryFile(t *testing.T) {
	d := newDevice(t)
	hist := ": 1700000000:0;docker compose up -d\n: 1700000001:0;pinner list\n"
	if err := os.WriteFile(filepath.Join(d.dir, ".zsh_history"), []byte(hist), 0o600); err != nil {
		t.Fatal(err)
	}
	d.ok("", "add", "--last")
	if got := d.commands(); len(got) != 1 || got[0] != "docker compose up -d" {
		t.Fatalf("got %q", got)
	}
}

func TestListMasksSensitivePins(t *testing.T) {
	d := newDevice(t)
	secret := "NOT-A-REAL-KEY-e2e-0001"
	d.ok("", "add", "--sensitive", "-n", "api", "--", "curl -H 'X-API-KEY: "+secret+"' https://api.example.com")
	if out := d.ok("", "list").stdout; strings.Contains(out, secret) || !strings.Contains(out, "sensitive") {
		t.Fatalf("list leaked or unflagged sensitive pin:\n%s", out)
	}
	if out := d.ok("", "list", "--show-sensitive").stdout; !strings.Contains(out, secret) {
		t.Fatalf("--show-sensitive should reveal it:\n%s", out)
	}
}

func TestUpdateAndRemoveByID(t *testing.T) {
	d := newDevice(t)
	d.ok("", "add", "echo one")
	d.ok("", "add", "echo two")
	d.ok("", "update", "1", "--label", "first", "--tag", "a,b", "--sensitive")
	d.ok("", "update", "1", "--command", "echo ONE")
	if r := d.run("", "update", "1", "--command", "echo two"); r.code == 0 {
		t.Fatal("updating to a duplicate command should fail")
	}
	e := d.export()
	if len(e.Pins) != 2 || e.Pins[0].Command != "echo ONE" || e.Pins[0].Label != "first" || !e.Pins[0].Sensitive {
		t.Fatalf("after update: %+v", e.Pins)
	}

	// --edit uses $EDITOR.
	ed := filepath.Join(d.dir, "ed.sh")
	if err := os.WriteFile(ed, []byte("#!/bin/sh\nsed -i.bak 's/two/TWO/' \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	d.env = append(d.env, "EDITOR="+ed, "VISUAL=")
	d.ok("", "update", "2", "--edit")
	d.ok("", "rm", "1", "-y")
	if got := d.commands(); len(got) != 1 || got[0] != "echo TWO" {
		t.Fatalf("got %q", got)
	}
	if r := d.run("", "rm", "1", "-y"); r.code == 0 {
		t.Fatal("removing a removed pin should fail")
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	a := newDevice(t)
	a.ok("", "add", "-n", "tunnel", "-t", "net", "--local", "--", "ssh -L <<port:5601>>:localhost:<<port>> host")
	a.ok("", "add", "--sensitive", "--", "echo secret-ish")
	a.ok("", "add", "echo plain")
	file := filepath.Join(a.dir, "pins.json")
	a.ok("", "export", "-o", file)
	info, _ := os.Stat(file)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("export file mode %o", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(file)
	if !strings.Contains(string(raw), "<<port:5601>>") {
		t.Fatalf("export should keep placeholders readable:\n%s", raw)
	}

	b := newDevice(t)
	b.ok("", "add", "echo plain")
	r := b.ok("", "import", file)
	if !strings.Contains(r.stderr, "Imported 2 pins (1 skipped") {
		t.Fatalf("import summary: %s", r.stderr)
	}
	got := b.export()
	if len(got.Pins) != 3 {
		t.Fatalf("pins after import: %+v", got.Pins)
	}
	for _, p := range got.Pins {
		switch p.Command {
		case "ssh -L <<port:5601>>:localhost:<<port>> host":
			if !p.Local || p.Label != "tunnel" || len(p.Tags) != 1 {
				t.Fatalf("flags lost on import: %+v", p)
			}
		case "echo secret-ish":
			if !p.Sensitive {
				t.Fatal("sensitive flag lost on import")
			}
		}
	}
}

func TestShellInitScriptsAreValid(t *testing.T) {
	d := newDevice(t)
	script := d.ok("", "init", "bash").stdout
	check := exec.Command("bash", "-n")
	check.Stdin = strings.NewReader(script)
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
	for _, sh := range []string{"zsh", "fish"} {
		script := d.ok("", "init", sh).stdout
		if _, err := exec.LookPath(sh); err != nil {
			continue
		}
		check := exec.Command(sh, "-n")
		check.Stdin = strings.NewReader(script)
		if out, err := check.CombinedOutput(); err != nil {
			t.Fatalf("%s -n: %v\n%s", sh, err, out)
		}
	}
	if r := d.run("", "init", "powershell"); r.code == 0 {
		t.Fatal("unknown shell should fail")
	}
}

// TestMigratesDatabaseFromOriginalRelease builds the last release before this change (which used the
// cgo sqlite driver), creates pins with it, and checks the new binary reads and upgrades that file.
func TestMigratesDatabaseFromOriginalRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the original release")
	}
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("needs a C compiler to build the original cgo release")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	src := t.TempDir()
	archive := exec.Command("sh", "-c", "git archive c4c84e3 | tar -x -C \"$1\"", "sh", src)
	archive.Dir = repoRoot
	if out, err := archive.CombinedOutput(); err != nil {
		t.Skipf("original release not available in this checkout: %v %s", err, out)
	}
	oldBin := filepath.Join(src, "pinner-old")
	build := exec.Command("go", "build", "-o", oldBin, ".")
	build.Dir = src
	build.Env = append(os.Environ(), "CGO_ENABLED=1")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building original release: %v\n%s", err, out)
	}

	home := t.TempDir()
	for _, c := range []string{"ls -la", "kubectl get pods -o go-template='{{range .items}}{{.metadata.name}}{{end}}'"} {
		add := exec.Command(oldBin, "add")
		add.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
		add.Stdin = strings.NewReader(c + "\n")
		if out, err := add.CombinedOutput(); err != nil {
			t.Fatalf("old add: %v %s", err, out)
		}
	}

	d := newDevice(t)
	d.env[0] = "PINNER_CONFIG_DIR=" + filepath.Join(home, ".config", "pinner")
	want := []string{"ls -la", "kubectl get pods -o go-template='{{range .items}}{{.metadata.name}}{{end}}'"}
	if got := d.commands(); !sameSet(got, want) {
		t.Fatalf("after migration got %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "pinner", "commands.db.bak")); err != nil {
		t.Fatalf("no pre-migration backup: %v", err)
	}
	d.ok("", "add", "echo new")
	d.ok("", "rm", "1", "-y")
	if got := d.commands(); len(got) != 2 {
		t.Fatalf("migrated database not writable: %q", got)
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func startServer(t *testing.T) (url, dbPath string) {
	t.Helper()
	addr := freePort(t)
	dbPath = filepath.Join(t.TempDir(), "server.db")
	srv := exec.Command(serverBin, "-addr", addr, "-db", dbPath)
	var logs bytes.Buffer
	srv.Stdout, srv.Stderr = &logs, &logs
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = srv.Process.Kill()
		_, _ = srv.Process.Wait()
		if t.Failed() {
			t.Logf("server logs:\n%s", logs.String())
		}
	})
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("tcp", addr); err == nil {
			_ = c.Close()
			return "http://" + addr, dbPath
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server did not start")
	return "", ""
}

func TestSyncBetweenDevicesThroughRealServer(t *testing.T) {
	url, dbPath := startServer(t)
	a, b := newDevice(t), newDevice(t)

	a.ok("", "add", "-n", "status", "--", "git status")
	a.ok("", "add", "--local", "--", "cat ~/.ssh/config")
	init := a.ok("", "sync", "init", "--server", url)
	secret := strings.TrimSpace(init.stdout)
	if !strings.HasPrefix(secret, "PINR-") {
		t.Fatalf("sync init should print the secret on stdout, got %q", init.stdout)
	}
	if info, err := os.Stat(filepath.Join(a.dir, "sync.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("sync.json missing or not 0600: %v", err)
	}

	b.ok("", "add", "git status") // same command already on b: must not duplicate
	b.ok("", "sync", "join", strings.ToLower(secret), "--server", url)
	if got := b.commands(); !sameSet(got, []string{"git status"}) {
		t.Fatalf("b after join: %q", got)
	}
	if b.export().Pins[0].Label != "status" {
		t.Fatal("b should have adopted a's label for the shared pin")
	}

	// Writes push automatically; reading on the other device after a sync shows them.
	b.ok("", "add", "-t", "ops", "--", "uptime")
	a.ok("", "sync")
	if got := a.commands(); !sameSet(got, []string{"git status", "cat ~/.ssh/config", "uptime"}) {
		t.Fatalf("a after sync: %q", got)
	}
	a.ok("", "rm", "3", "-y") // pin #3 on a is "uptime", pulled from b
	b.ok("", "sync")
	if got := b.commands(); !sameSet(got, []string{"git status"}) {
		t.Fatalf("b after remote delete: %q", got)
	}

	status := a.ok("", "sync", "status").stdout
	if !strings.Contains(status, "Pending changes: 0") || !strings.Contains(status, "1 local-only") {
		t.Fatalf("status:\n%s", status)
	}
	if key := a.ok("", "sync", "key").stdout; strings.TrimSpace(key) != secret {
		t.Fatalf("sync key printed %q", key)
	}

	// The server's database never contains pin contents.
	raw, _ := os.ReadFile(dbPath)
	wal, _ := os.ReadFile(dbPath + "-wal")
	for _, needle := range []string{"git status", "uptime", "ssh/config", "status"} {
		if bytes.Contains(raw, []byte(needle)) || bytes.Contains(wal, []byte(needle)) {
			t.Fatalf("server storage contains plaintext %q", needle)
		}
	}

	// leave --purge wipes the server copy; a third device joining afterwards gets nothing.
	a.ok("", "sync", "leave", "--purge")
	if got := a.commands(); len(got) != 2 {
		t.Fatalf("leaving must keep local pins, got %q", got)
	}
	c := newDevice(t)
	c.ok("", "sync", "join", secret, "--server", url)
	if got := c.commands(); len(got) != 0 {
		t.Fatalf("purged account still had data: %q", got)
	}
}

func TestSyncRejectsBadInput(t *testing.T) {
	url, _ := startServer(t)
	d := newDevice(t)
	if r := d.run("", "sync", "join", "PINR-AAAA-BBBB", "--server", url); r.code == 0 || !strings.Contains(r.stderr, "invalid sync secret") {
		t.Fatalf("bad secret: %+v", r)
	}
	if r := d.run("", "sync", "init", "--server", "http://pins.example.com"); r.code == 0 || !strings.Contains(r.stderr, "https") {
		t.Fatalf("plain http to a remote host should be refused: %+v", r)
	}
	if r := d.run("", "sync"); r.code == 0 || !strings.Contains(r.stderr, "not set up") {
		t.Fatalf("sync without setup: %+v", r)
	}
	d.ok("", "sync", "init", "--server", url)
	if r := d.run("", "sync", "init", "--server", url); r.code == 0 {
		t.Fatal("init twice should fail")
	}
}

func TestCommandsKeepWorkingWhenServerIsDown(t *testing.T) {
	url, _ := startServer(t)
	d := newDevice(t)
	d.ok("", "sync", "init", "--server", url)
	// Point the config at a port nobody listens on.
	cfgPath := filepath.Join(d.dir, "sync.json")
	cfg, _ := os.ReadFile(cfgPath)
	dead := "http://" + freePort(t)
	if err := os.WriteFile(cfgPath, bytes.Replace(cfg, []byte(url), []byte(dead), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r := d.ok("", "add", "echo offline")
	if time.Since(start) > 5*time.Second {
		t.Fatalf("add blocked on the dead server for %v", time.Since(start))
	}
	if !strings.Contains(r.stderr, "sync skipped") {
		t.Fatalf("expected an offline warning, got %q", r.stderr)
	}
	d.env = append(d.env, "PINNER_SYNC=off")
	if r := d.ok("", "add", "echo quiet"); strings.Contains(r.stderr, "sync") {
		t.Fatalf("PINNER_SYNC=off should silence sync: %q", r.stderr)
	}
	if status := d.ok("", "sync", "status").stdout; !strings.Contains(status, "Pending changes: 2") {
		t.Fatalf("unsynced changes should be pending:\n%s", status)
	}
}

// ---- Interactive tests (tmux) ----

var tmuxSessions atomic.Int64

type tmux struct {
	t       *testing.T
	socket  string
	session string
}

func newTmux(t *testing.T, d *device, width int, command string) *tmux {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("tmux tests need a unix terminal")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	// A fresh socket per session: reusing one right after kill-server races with the old server's
	// shutdown, and the new session can attach to the dying server ("server exited unexpectedly").
	tm := &tmux{t: t, socket: fmt.Sprintf("pinner-e2e-%d-%d", os.Getpid(), tmuxSessions.Add(1)), session: "s"}
	envs := strings.Join(d.env, " ")
	script := filepath.Join(d.dir, "tmux-cmd.sh")
	body := fmt.Sprintf("#!/bin/sh\nexport %s PATH=%s:$PATH TERM=xterm-256color\nsleep 0.3\n%s\nsleep 60\n", envs, binDir, command)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	tm.cmd("new-session", "-d", "-s", tm.session, "-x", fmt.Sprint(width), "-y", "30", script)
	tm.cmd("resize-window", "-t", tm.session, "-x", fmt.Sprint(width), "-y", "30")
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", tm.socket, "kill-server").Run() })
	time.Sleep(700 * time.Millisecond)
	return tm
}

func (tm *tmux) cmd(args ...string) string {
	tm.t.Helper()
	out, err := exec.Command("tmux", append([]string{"-L", tm.socket}, args...)...).CombinedOutput()
	if err != nil {
		tm.t.Fatalf("tmux %v: %v %s", args, err, out)
	}
	return string(out)
}

func (tm *tmux) keys(keys ...string) {
	tm.t.Helper()
	for _, k := range keys {
		tm.cmd("send-keys", "-t", tm.session, k)
		time.Sleep(60 * time.Millisecond)
	}
}

// waitScreen waits until text appears on the tmux screen.
func (tm *tmux) waitScreen(text string) {
	tm.t.Helper()
	for i := 0; i < 100; i++ {
		if strings.Contains(tm.screen(), text) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	tm.t.Fatalf("%q never appeared on screen:\n%s", text, tm.screen())
}

func (tm *tmux) screen() string {
	tm.t.Helper()
	return tm.cmd("capture-pane", "-t", tm.session, "-p")
}

// waitFile waits for a file the command under test writes when it finishes.
func waitFile(t *testing.T, path string) string {
	t.Helper()
	for i := 0; i < 100; i++ {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 && bytes.HasSuffix(b, []byte("\n")) {
			return string(b)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s was never written", path)
	return ""
}

func TestMenuDoesNotDuplicateLinesWhenScrollingPastLongCommands(t *testing.T) {
	d := newDevice(t)
	for i := 1; i <= 15; i++ {
		c := fmt.Sprintf("echo command number %d", i)
		if i == 7 {
			c = "curl -X POST https://example.com/search -H 'Content-Type: application/json' -H 'X-API-KEY: abcdefghijklmnopqrstuvwxyz0123456789' -d '{}'"
		}
		d.ok("", "add", "--", c)
	}
	for _, sub := range []string{"", "update", "remove"} {
		tm := newTmux(t, d, 80, "pinner "+sub)
		for i := 0; i < 25; i++ {
			tm.keys("Down")
		}
		for i := 0; i < 25; i++ {
			tm.keys("Up")
		}
		time.Sleep(300 * time.Millisecond)
		if n := strings.Count(tm.screen(), "Use the arrow keys"); n != 1 {
			t.Fatalf("pinner %s: help line appears %d times:\n%s", sub, n, tm.screen())
		}
		_ = exec.Command("tmux", "-L", tm.socket, "kill-server").Run()
	}
}

func TestPrintSelectsFrecencySortedPinAndFillsPlaceholders(t *testing.T) {
	d := newDevice(t)
	d.ok("", "add", "echo first")
	d.ok("", "add", "echo hello <<name:world>>")
	out := filepath.Join(d.dir, "out")

	// Select the second pin. --print writes only the command to stdout; the menu is on stderr.
	tm := newTmux(t, d, 100, "pinner --print > "+out+"; echo done >> "+out)
	tm.keys("Down", "Enter")
	time.Sleep(300 * time.Millisecond)
	if !strings.Contains(tm.screen(), "name") {
		t.Fatalf("expected a placeholder prompt:\n%s", tm.screen())
	}
	tm.keys("pinner", "Enter")
	// A new value: pinner offers to save it. Enter keeps the default action, "Run".
	tm.waitScreen("You entered new values")
	tm.keys("Enter")
	if got := waitFile(t, out); got != "echo hello pinner\ndone\n" {
		t.Fatalf("stdout was %q", got)
	}
	_ = exec.Command("tmux", "-L", tm.socket, "kill-server").Run()

	// That pin is now the most used, so it is first and Enter picks it. The prompt is pre-filled with
	// the value from last time, and repeating it doesn't ask about saving.
	_ = os.Remove(out)
	tm = newTmux(t, d, 100, "pinner --print > "+out+"; echo done >> "+out)
	tm.keys("Enter")
	tm.waitScreen("name [pinner]")
	tm.keys("Enter")
	if got := waitFile(t, out); got != "echo hello pinner\ndone\n" {
		t.Fatalf("after reordering, stdout was %q", got)
	}
	if strings.Contains(tm.screen(), "You entered new values") {
		t.Fatalf("repeating the last values should not ask about saving:\n%s", tm.screen())
	}
	if got := d.commands(); !sameSet(got, []string{"echo first", "echo hello <<name:world>>"}) {
		t.Fatalf("choosing Run must not change the pin: %q", got)
	}
}

// runPrint opens the menu with --print in tmux, runs drive against it, and returns what was printed.
func runPrint(t *testing.T, d *device, env string, drive func(tm *tmux)) (string, *tmux) {
	t.Helper()
	out := filepath.Join(d.dir, "out")
	_ = os.Remove(out)
	tm := newTmux(t, d, 120, env+" pinner --print > "+out+"; echo done >> "+out)
	drive(tm)
	got := strings.TrimSuffix(waitFile(t, out), "done\n")
	return got, tm
}

func pinByLabel(t *testing.T, d *device) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, p := range d.export().Pins {
		m[p.Label] = p.Command
	}
	return m
}

func TestPlaceholderRunAndSaveAsDefaults(t *testing.T) {
	d := newDevice(t)
	d.ok("", "add", "-n", "greet", "--", "echo <<greeting:hi>> <<who>> <<greeting>>")
	got, _ := runPrint(t, d, "", func(tm *tmux) {
		tm.keys("Enter")
		tm.waitScreen("greeting [hi]")
		tm.keys("hey", "Enter")
		tm.waitScreen("who")
		tm.keys("bob", "Enter")
		tm.waitScreen("You entered new values")
		tm.keys("Down", "Enter")
	})
	if got != "echo hey bob hey\n" {
		t.Fatalf("printed %q", got)
	}
	if cmd := pinByLabel(t, d)["greet"]; cmd != "echo <<greeting:hey>> <<who:bob>> <<greeting:hey>>" {
		t.Fatalf("defaults not saved into the pin: %q", cmd)
	}
	if len(d.export().Pins) != 1 {
		t.Fatal("saving defaults must not create another pin")
	}
}

func TestPlaceholderSaveAsNewPin(t *testing.T) {
	d := newDevice(t)
	d.ok("", "add", "-n", "tunnel", "-t", "net", "--", "ssh <<host>> -L <<port:5601>>:localhost:<<port>>")
	got, _ := runPrint(t, d, "", func(tm *tmux) {
		tm.keys("Enter")
		tm.waitScreen("host")
		tm.keys("staging.internal", "Enter")
		tm.waitScreen("port [5601]")
		tm.keys("Enter")
		tm.waitScreen("You entered new values")
		tm.keys("Down", "Down", "Enter")
		tm.waitScreen("Label for the new pin")
		tm.keys(" staging", "Enter")
	})
	if got != "ssh staging.internal -L 5601:localhost:5601\n" {
		t.Fatalf("printed %q", got)
	}
	pins := d.export().Pins
	if len(pins) != 2 {
		t.Fatalf("want the original and a new pin, got %+v", pins)
	}
	byLabel := pinByLabel(t, d)
	if byLabel["tunnel"] != "ssh <<host>> -L <<port:5601>>:localhost:<<port>>" {
		t.Fatalf("original pin changed: %q", byLabel["tunnel"])
	}
	if byLabel["tunnel staging"] != "ssh staging.internal -L 5601:localhost:5601" {
		t.Fatalf("new pin: %+v", pins)
	}
	for _, p := range pins {
		if p.Label == "tunnel staging" && (len(p.Tags) != 1 || p.Tags[0] != "net") {
			t.Fatalf("new pin should copy tags: %+v", p)
		}
	}
}

func TestPlaceholderQuestionCanBeTurnedOff(t *testing.T) {
	d := newDevice(t)
	d.ok("", "add", "echo <<x>>")
	got, tm := runPrint(t, d, "PINNER_ASK_SAVE=0", func(tm *tmux) {
		tm.keys("Enter")
		tm.waitScreen("x")
		tm.keys("one", "Enter")
	})
	if got != "echo one\n" || strings.Contains(tm.screen(), "You entered new values") {
		t.Fatalf("PINNER_ASK_SAVE=0 should skip the question; printed %q\n%s", got, tm.screen())
	}
	// Values are still remembered.
	got, _ = runPrint(t, d, "PINNER_ASK_SAVE=0", func(tm *tmux) {
		tm.keys("Enter")
		tm.waitScreen("x [one]")
		tm.keys("Enter")
	})
	if got != "echo one\n" {
		t.Fatalf("second run printed %q", got)
	}
}

func TestSensitivePinsDoNotRememberValues(t *testing.T) {
	d := newDevice(t)
	d.ok("", "add", "--sensitive", "--", "curl -H 'Authorization: Bearer <<token:NOT-A-REAL-DEFAULT>>' https://api.example.com")
	token := "NOT-A-REAL-TOKEN-typed-123"
	got, tm := runPrint(t, d, "", func(tm *tmux) {
		tm.keys("Enter")
		tm.waitScreen("token [default]") // the default isn't printed for sensitive pins
		tm.keys(token, "Enter")
		tm.waitScreen("You entered new values")
		tm.keys("Enter")
	})
	if !strings.Contains(got, token) {
		t.Fatalf("printed %q", got)
	}
	if strings.Contains(tm.screen(), "NOT-A-REAL-DEFAULT") {
		t.Fatalf("sensitive default shown on screen:\n%s", tm.screen())
	}
	got, tm = runPrint(t, d, "", func(tm *tmux) {
		tm.keys("Enter")
		tm.waitScreen("token [default]")
		tm.keys("Enter")
	})
	if strings.Contains(got, token) || !strings.Contains(got, "NOT-A-REAL-DEFAULT") {
		t.Fatalf("sensitive pin remembered the typed value: %q", got)
	}
	if strings.Contains(tm.screen(), token) {
		t.Fatalf("typed token shown on the second run:\n%s", tm.screen())
	}
}

func TestRunPropagatesExitCodeAndNeverShowsSensitiveCommand(t *testing.T) {
	d := newDevice(t)
	secret := "NOT-A-REAL-KEY-tmux-42"
	d.ok("", "add", "--sensitive", "--", "echo "+secret+" > ran.txt; exit 3")
	rc := filepath.Join(d.dir, "rc")
	tm := newTmux(t, d, 100, "cd "+d.dir+"; pinner; echo $? > "+rc)
	tm.keys("Enter")
	if got := waitFile(t, rc); got != "3\n" {
		t.Fatalf("exit code %q, want 3", got)
	}
	if strings.Contains(tm.screen(), secret) {
		t.Fatalf("sensitive command visible on screen:\n%s", tm.screen())
	}
	if b, _ := os.ReadFile(filepath.Join(d.dir, "ran.txt")); strings.TrimSpace(string(b)) != secret {
		t.Fatalf("the full command should still run, ran.txt=%q", b)
	}
}

func TestBashIntegration(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	d := newDevice(t)
	d.ok("", "add", "echo from-widget")
	rc := filepath.Join(d.dir, "bashrc")
	if err := os.WriteFile(rc, []byte("PS1='$ '\neval \"$(pinner init bash)\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tm := newTmux(t, d, 100, "bash --noprofile --rcfile "+rc+" -i")
	tm.keys("echo just-ran-this", "Enter")
	tm.keys("pin-last -n recent", "Enter")
	time.Sleep(500 * time.Millisecond)

	// Alt+p opens the picker; Enter inserts the pin into the prompt without running it.
	tm.keys("M-p")
	time.Sleep(500 * time.Millisecond)
	tm.keys("Enter")
	time.Sleep(500 * time.Millisecond)
	screen := tm.screen()
	lines := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "$ ") || !strings.Contains(last, "echo ") {
		t.Fatalf("widget should leave a pin on the prompt line, screen:\n%s", screen)
	}

	e := d.export()
	found := false
	for _, p := range e.Pins {
		if p.Command == "echo just-ran-this" && p.Label == "recent" {
			found = true
		}
	}
	if !found {
		t.Fatalf("pin-last did not pin the previous command: %+v\nscreen:\n%s", e.Pins, screen)
	}
}
