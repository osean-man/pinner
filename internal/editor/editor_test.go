package editor

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeEditor writes a shell script that behaves like an editor and returns its path.
func fakeEditor(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell script as the editor")
	}
	p := filepath.Join(t.TempDir(), "fake-editor")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEditReturnsSavedMultilineText(t *testing.T) {
	ed := fakeEditor(t, `
mode=$(stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1")
[ "$mode" = "600" ] || { echo "temp file mode $mode" >&2; exit 3; }
grep -q '^old command$' "$1" || exit 4
printf 'for f in *.log; do\n  gzip "$f"\ndone\n\n' > "$1"
echo "$1" > "$(dirname "$0")/seen-path"`)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", ed)
	got, err := Edit("old command\n")
	if err != nil {
		t.Fatal(err)
	}
	if got != "for f in *.log; do\n  gzip \"$f\"\ndone" {
		t.Fatalf("got %q", got)
	}
	seen, _ := os.ReadFile(filepath.Join(filepath.Dir(ed), "seen-path"))
	if _, err := os.Stat(string(seen[:len(seen)-1])); !os.IsNotExist(err) {
		t.Fatal("temp file holding the command was not removed")
	}
}

func TestEditorWithArguments(t *testing.T) {
	ed := fakeEditor(t, `[ "$1" = "--wait" ] || exit 5; echo edited > "$2"`)
	t.Setenv("VISUAL", ed+" --wait")
	got, err := Edit("x")
	if err != nil || got != "edited" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestEditAbortsOnFailureOrEmpty(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", fakeEditor(t, "exit 1"))
	if _, err := Edit("x"); !errors.Is(err, ErrAborted) {
		t.Fatalf("failing editor: %v", err)
	}
	t.Setenv("EDITOR", fakeEditor(t, `: > "$1"`))
	if _, err := Edit("x"); !errors.Is(err, ErrAborted) {
		t.Fatalf("emptied file: %v", err)
	}
}
