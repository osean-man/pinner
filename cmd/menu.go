package cmd

import (
	"os"

	"github.com/manifoldco/promptui"
	"github.com/osean-man/pinner/internal/database"
)

// selectPin shows an interactive, searchable menu of pins and returns the chosen one.
// The menu is drawn on stderr so that stdout stays clean for --print.
func selectPin(pins []database.Pin, label, selectedIcon string) (database.Pin, error) {
	items := pinItems(pins)
	prompt := promptui.Select{
		Label: label,
		Items: items,
		Templates: &promptui.SelectTemplates{
			Label:    "{{ . }}",
			Active:   "📌 {{ .Display | green }}",
			Inactive: "  {{ .Display }}",
			Selected: selectedIcon + " {{ .Display | cyan }}",
		},
		Size:     10,
		Searcher: func(input string, index int) bool { return items[index].matches(input) },
		Stdout:   os.Stderr,
	}
	index, _, err := prompt.Run()
	if err != nil {
		return database.Pin{}, err
	}
	// items was built from pins in the same order, so the index refers to the same pin.
	return pins[index], nil
}
