package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var removeYes bool

var removeCmd = &cobra.Command{
	Use:     "remove [id]",
	Aliases: []string{"rm"},
	Short:   "Remove a pinned command",
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		pin, err := pickPin(args, "Select a command to remove", "✖️ ")
		if err != nil {
			return err
		}
		if !removeYes {
			ok, err := confirm(fmt.Sprintf("Remove %s", displayCommand(pinText(pin), terminalWidth()-20)))
			if err != nil || !ok {
				if err == nil {
					_, _ = fmt.Fprintln(os.Stderr, "Removal cancelled.")
				}
				return err
			}
		}
		if err := store.Remove(pin.ID); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(os.Stderr, "Command removed successfully!")
		autoSync(writeSyncTimeout)
		return nil
	},
}

func init() {
	removeCmd.Flags().BoolVarP(&removeYes, "yes", "y", false, "don't ask for confirmation")
	rootCmd.AddCommand(removeCmd)
}
