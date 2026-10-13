package cmd

import (
	"os"
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/osean-man/pinner/internal/database"
	"golang.org/x/term"
)

// selectRowPrefixWidth is the widest prefix promptui puts before a command in the select list:
// the "↑"/"↓" page indicator plus a space (2 columns), then the active template's "📌 " (3 columns).
// One extra column is reserved so nothing is written into the terminal's last column.
const selectRowPrefixWidth = 6

// Item is a pin as shown in a select menu.
type Item struct {
	ID      int64
	Display string
	search  string
}

// terminalWidth returns the width of the terminal the menu is drawn on (stderr), or 80 if unknown.
func terminalWidth() int {
	for _, f := range []*os.File{os.Stderr, os.Stdout} {
		if width, _, err := term.GetSize(int(f.Fd())); err == nil && width > 0 {
			return width
		}
	}
	return 80
}

// displayCommand flattens a command onto a single line and truncates it to maxWidth columns.
// promptui redraws the list by moving the cursor up one row per item, so an item that wraps
// onto a second row leaves stale lines behind on every redraw.
func displayCommand(command string, maxWidth int) string {
	flat := strings.Join(strings.Fields(command), " ")
	if maxWidth < 1 {
		maxWidth = 1
	}
	return runewidth.Truncate(flat, maxWidth, "…")
}

// maskedCommand hides everything after the program name of a sensitive command.
func maskedCommand(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "••••••"
	}
	if len(fields) == 1 {
		return "••••••"
	}
	return fields[0] + " ••••••"
}

// pinText is the one-line text shown for a pin: "[label] command", with the command masked if sensitive.
func pinText(p database.Pin) string {
	cmd := p.Command
	if p.Sensitive {
		cmd = "🔒 " + maskedCommand(cmd)
	}
	if p.Label != "" {
		return "[" + p.Label + "] " + cmd
	}
	return cmd
}

// pinItems converts pins into menu items whose Display fits the terminal. Items are in the same
// order as pins, so the index the menu returns can be used to index pins.
func pinItems(pins []database.Pin) []Item {
	maxWidth := terminalWidth() - selectRowPrefixWidth
	items := make([]Item, len(pins))
	for i, p := range pins {
		items[i] = Item{
			ID:      p.ID,
			Display: displayCommand(pinText(p), maxWidth),
			search:  strings.ToLower(p.Command + " " + p.Label + " " + strings.Join(p.Tags, " ")),
		}
	}
	return items
}

// matches reports whether every whitespace-separated term in input appears in the item's command, label or tags.
func (it Item) matches(input string) bool {
	for _, term := range strings.Fields(strings.ToLower(input)) {
		if !strings.Contains(it.search, term) {
			return false
		}
	}
	return true
}
