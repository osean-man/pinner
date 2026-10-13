package history

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBashPlainAndTimestamped(t *testing.T) {
	plain := "ls -la\ncd /tmp\ngit status\n"
	if got := Entries([]byte(plain), ".bash_history"); !reflect.DeepEqual(got, []string{"ls -la", "cd /tmp", "git status"}) {
		t.Fatalf("plain: %q", got)
	}
	stamped := "#1700000000\nls -la\n#1700000005\nmake test\n"
	if got := Entries([]byte(stamped), ".bash_history"); !reflect.DeepEqual(got, []string{"ls -la", "make test"}) {
		t.Fatalf("timestamped: %q", got)
	}
}

func TestZshExtendedWithMultiline(t *testing.T) {
	data := ": 1700000000:0;ls -la\n" +
		": 1700000010:3;for i in 1 2; do\\\n  echo $i\\\ndone\n" +
		": 1700000020:0;echo 'trailing backslash escaped \\\\'\n" +
		": 1700000030:0;git push\n"
	got := Entries([]byte(data), ".zsh_history")
	want := []string{"ls -la", "for i in 1 2; do\n  echo $i\ndone", "echo 'trailing backslash escaped \\\\'", "git push"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestZshMetafiedUnicode(t *testing.T) {
	// zsh stores bytes >= 0x83 as 0x83 followed by byte^0x20. "é" is 0xC3 0xA9; 0xC3 is metafied.
	raw := []byte(": 1700000000:0;echo caf")
	raw = append(raw, 0x83, 0xC3^0x20, 0xA9, '\n')
	if got := Entries(raw, ".zsh_history"); len(got) != 1 || got[0] != "echo café" {
		t.Fatalf("got %q", got)
	}
}

func TestFish(t *testing.T) {
	data := "- cmd: ls -la\n  when: 1700000000\n- cmd: echo a\\nb \\\\ c\n  when: 1700000001\n  paths:\n    - /tmp\n"
	got := Entries([]byte(data), "fish_history")
	if !reflect.DeepEqual(got, []string{"ls -la", "echo a\nb \\ c"}) {
		t.Fatalf("got %q", got)
	}
}

func TestLastSkipsPinnerInvocations(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, ".zsh_history", ": 1:0;kubectl get pods\n: 2:0;pinner add --last\n: 3:0;   \n")
	got, err := Last(p, func(c string) bool { return strings.HasPrefix(c, "pinner") })
	if err != nil || got != "kubectl get pods" {
		t.Fatalf("got %q, %v", got, err)
	}
	empty := write(t, dir, "empty", "")
	if _, err := Last(empty, nil); err != ErrNoHistory {
		t.Fatalf("empty history: %v", err)
	}
}

func TestMostRecentFile(t *testing.T) {
	dir := t.TempDir()
	old := write(t, dir, ".bash_history", "a\n")
	newer := write(t, dir, ".zsh_history", ": 1:0;b\n")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	got, err := MostRecentFile([]string{filepath.Join(dir, "missing"), old, newer})
	if err != nil || got != newer {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := MostRecentFile([]string{filepath.Join(dir, "missing")}); err != ErrNoHistory {
		t.Fatalf("no files: %v", err)
	}
}
