package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/spf13/cobra"
)

var (
	listTag  string
	listShow bool
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all pinned commands",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		pins, err := store.List(listTag)
		if err != nil {
			return err
		}
		if len(pins) == 0 {
			_, _ = fmt.Fprintln(os.Stderr, "No pinned commands found.")
			return nil
		}

		t := table.NewWriter()
		t.SetOutputMirror(os.Stdout)
		t.SetStyle(table.StyleLight)
		t.AppendHeader(table.Row{"ID", "Label", "Command", "Tags", "Uses", "Flags"})
		for _, p := range pins {
			command := p.Command
			if p.Sensitive && !listShow {
				command = maskedCommand(command)
			}
			var flags []string
			if p.Sensitive {
				flags = append(flags, "sensitive")
			}
			if p.Local {
				flags = append(flags, "local")
			}
			t.AppendRow(table.Row{p.ID, p.Label, command, strings.Join(p.Tags, ","), p.UseCount, strings.Join(flags, ",")})
		}
		t.Render()
		return nil
	},
}

func init() {
	listCmd.Flags().StringVarP(&listTag, "tag", "t", "", "only list pins with this tag")
	listCmd.Flags().BoolVar(&listShow, "show-sensitive", false, "show sensitive commands in full")
	rootCmd.AddCommand(listCmd)
}
