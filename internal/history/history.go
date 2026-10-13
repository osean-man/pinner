// Package history reads the most recent command from a shell history file (bash, zsh or fish).
//
// Bash only writes its history file when the shell exits (unless `history -a` runs in PROMPT_COMMAND),
// so the shell integration from `pinner init` is the more reliable way to pin the last command.
package history

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoHistory is returned when no usable history entry is found.
var ErrNoHistory = errors.New("no shell history found")

// Candidates returns history files that may exist, in priority order: $HISTFILE first, then the
// default files for zsh, bash and fish.
func Candidates() []string {
	var out []string
	if h := os.Getenv("HISTFILE"); h != "" {
		out = append(out, h)
	}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out,
			filepath.Join(home, ".zsh_history"),
			filepath.Join(home, ".bash_history"),
			filepath.Join(home, ".local", "share", "fish", "fish_history"),
		)
	}
	return out
}

// MostRecentFile returns the most recently modified existing file among paths.
func MostRecentFile(paths []string) (string, error) {
	var best string
	var bestTime int64
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil || info.IsDir() {
			continue
		}
		if t := info.ModTime().UnixNano(); best == "" || t > bestTime {
			best, bestTime = p, t
		}
	}
	if best == "" {
		return "", ErrNoHistory
	}
	return best, nil
}

// Entries parses a history file into commands, oldest first.
func Entries(data []byte, path string) []string {
	if strings.Contains(filepath.Base(path), "fish") {
		return parseFish(data)
	}
	return parseShell(data)
}

// Last returns the newest command in the history file at path for which skip returns false.
func Last(path string, skip func(string) bool) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	entries := Entries(data, path)
	for i := len(entries) - 1; i >= 0; i-- {
		e := strings.TrimSpace(entries[i])
		if e != "" && (skip == nil || !skip(e)) {
			return e, nil
		}
	}
	return "", ErrNoHistory
}

// unmetafy reverses zsh's encoding of bytes >= 0x83 in history files.
func unmetafy(b []byte) []byte {
	if bytes.IndexByte(b, 0x83) < 0 {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] == 0x83 && i+1 < len(b) {
			i++
			out = append(out, b[i]^0x20)
			continue
		}
		out = append(out, b[i])
	}
	return out
}

// parseShell handles bash (optionally with "#<epoch>" timestamp lines) and zsh
// (optionally extended ": <epoch>:<dur>;cmd" format, with backslash-continued multi-line entries).
func parseShell(data []byte) []string {
	data = unmetafy(data)
	var entries []string
	var cur strings.Builder
	continuing := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !continuing {
			if isBashTimestamp(line) {
				continue
			}
			if rest, ok := zshExtended(line); ok {
				line = rest
			}
			cur.Reset()
		} else {
			cur.WriteByte('\n')
		}
		if strings.HasSuffix(line, "\\") && !strings.HasSuffix(line, "\\\\") {
			cur.WriteString(strings.TrimSuffix(line, "\\"))
			continuing = true
			continue
		}
		cur.WriteString(line)
		continuing = false
		entries = append(entries, cur.String())
	}
	if continuing {
		entries = append(entries, cur.String())
	}
	return entries
}

func isBashTimestamp(line string) bool {
	if len(line) < 2 || line[0] != '#' {
		return false
	}
	for _, c := range line[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func zshExtended(line string) (string, bool) {
	if !strings.HasPrefix(line, ": ") {
		return "", false
	}
	i := strings.IndexByte(line, ';')
	if i < 0 {
		return "", false
	}
	meta := line[2:i] // "<epoch>:<duration>"
	parts := strings.SplitN(meta, ":", 2)
	if len(parts) != 2 || !isDigits(parts[0]) || !isDigits(parts[1]) {
		return "", false
	}
	return line[i+1:], true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// parseFish handles fish's YAML-like history: "- cmd: ..." lines with \n and \\ escapes.
func parseFish(data []byte) []string {
	var entries []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if cmd, ok := strings.CutPrefix(line, "- cmd: "); ok {
			entries = append(entries, unescapeFish(cmd))
		}
	}
	return entries
}

func unescapeFish(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case '\\':
				b.WriteByte('\\')
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
