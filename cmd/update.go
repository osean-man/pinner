package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/manifoldco/promptui"
	"github.com/osean-man/pinner/internal/database"
	"github.com/osean-man/pinner/internal/editor"
	"github.com/spf13/cobra"
)

var (
	updateFlags   pinFlags
	updateEdit    bool
	updateCommand string
)

var updateCmd = &cobra.Command{
	Use:   "update [id]",
	Short: "Update a pinned command",
	Long: `Update a pinned command. Without flags, you are prompted for the new command, label and tags.
With flags, only the given fields change and nothing is prompted.`,
	Example: `  pinner update                          # pick from the menu, edit interactively
  pinner update 4 --edit                 # edit pin #4 in $EDITOR
  pinner update 4 --label "db shell" --tag db,prod
  pinner update 4 --sensitive=false --local`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		pin, err := pickPin(args, "Select a command to update", "🖊️ ")
		if err != nil {
			return err
		}

		var u database.PinUpdate
		flags := cmd.Flags()
		if flags.Changed("command") {
			u.Command = &updateCommand
		}
		if flags.Changed("label") {
			u.Label = &updateFlags.label
		}
		if flags.Changed("tag") {
			u.Tags = &updateFlags.tags
		}
		if flags.Changed("sensitive") {
			u.Sensitive = &updateFlags.sensitive
		}
		if flags.Changed("local") {
			u.Local = &updateFlags.local
		}

		switch {
		case updateEdit:
			edited, err := editor.Edit(pin.Command + "\n")
			if err != nil {
				return err
			}
			u.Command = &edited
		case flags.NFlag() == 0:
			if err := promptUpdate(pin, &u); err != nil {
				return err
			}
		}

		updated, err := store.Update(pin.ID, u)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(os.Stderr, "Updated #%d: %s\n", updated.ID, pinText(updated))
		autoSync(writeSyncTimeout)
		return nil
	},
}

func promptUpdate(pin database.Pin, u *database.PinUpdate) error {
	fields := []struct {
		label string
		value string
		set   func(string)
	}{
		{"Command", pin.Command, func(v string) { u.Command = &v }},
		{"Label", pin.Label, func(v string) { u.Label = &v }},
		{"Tags (comma-separated)", strings.Join(pin.Tags, ","), func(v string) {
			tags := database.SplitTags(v)
			u.Tags = &tags
		}},
	}
	if pin.Sensitive {
		// Don't pre-fill a sensitive command on screen; leave it empty to keep it.
		fields[0].label, fields[0].value = "Command (sensitive: leave empty to keep)", ""
		fields[0].set = func(v string) {
			if strings.TrimSpace(v) != "" {
				u.Command = &v
			}
		}
	}
	for _, f := range fields {
		prompt := promptui.Prompt{Label: f.label, Default: f.value, AllowEdit: true, Stdout: os.Stderr}
		v, err := prompt.Run()
		if err != nil {
			return err
		}
		f.set(v)
	}
	return nil
}

// pickPin returns the pin whose ID is in args, or lets the user choose one from the menu.
func pickPin(args []string, label, icon string) (database.Pin, error) {
	if len(args) == 1 {
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return database.Pin{}, fmt.Errorf("invalid pin id %q (see pinner list)", args[0])
		}
		return store.Get(id)
	}
	pins, err := store.List("")
	if err != nil {
		return database.Pin{}, err
	}
	if len(pins) == 0 {
		return database.Pin{}, fmt.Errorf("you have no pinned commands")
	}
	return selectPin(pins, label, icon)
}

func init() {
	addPinFlags(updateCmd, &updateFlags)
	updateCmd.Flags().StringVar(&updateCommand, "command", "", "replace the command text")
	updateCmd.Flags().BoolVarP(&updateEdit, "edit", "e", false, "edit the command in $EDITOR")
	updateCmd.MarkFlagsMutuallyExclusive("edit", "command")
	rootCmd.AddCommand(updateCmd)
}
