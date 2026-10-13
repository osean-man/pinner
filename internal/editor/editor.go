// Package editor edits text in the user's $VISUAL or $EDITOR.
package editor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// ErrAborted is returned when the editor exits with an error or the result is empty.
var ErrAborted = errors.New("edit aborted: the editor failed or the command was left empty")

// Command returns the editor to use: $VISUAL, then $EDITOR, then vi (notepad on Windows).
func Command() string {
	for _, k := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	if runtime.GOOS == "windows" {
		return "notepad"
	}
	return "vi"
}

// Edit opens initial in the editor and returns the saved text with surrounding whitespace trimmed.
// The temp file is private (0600) because pins can contain secrets, and is removed afterwards.
func Edit(initial string) (string, error) {
	f, err := os.CreateTemp("", "pinner-*.sh")
	if err != nil {
		return "", err
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()
	if err := f.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		_ = f.Close()
		return "", err
	}
	if _, err := f.WriteString(initial); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", Command()+" "+path)
	} else {
		// Run through sh so editors configured with arguments (e.g. "code --wait") work.
		cmd = exec.Command("sh", "-c", Command()+` "$1"`, "pinner-editor", path)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w (%v)", ErrAborted, err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "", ErrAborted
	}
	return text, nil
}
