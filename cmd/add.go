package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/manifoldco/promptui"
	"github.com/osean-man/pinner/internal/database"
	"github.com/osean-man/pinner/internal/editor"
	"github.com/osean-man/pinner/internal/history"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type pinFlags struct {
	label     string
	tags      []string
	sensitive bool
	local     bool
}

var (
	addFlags pinFlags
	addEdit  bool
	addLast  bool
)

var addCmd = &cobra.Command{
	Use:   "add [command...]",
	Short: "Add a new pinned command",
	Long: `Add a new pinned command. The command can be given as arguments, piped in, typed at a prompt,
written in your $EDITOR (--edit) or taken from your shell history (--last).

Use <<name>> or <<name:default>> placeholders to be prompted for values when the pin runs.`,
	Example: `  pinner add 'kubectl logs -l app=<<app>> -n <<ns:default>> -f' -n "tail logs" -t k8s
  echo "ls -la" | pinner add
  pinner add --sensitive -n "prod api" -- curl -H "X-API-KEY: ..." https://api.example.com
  pinner add --last
  pinner add --edit`,
	RunE: func(cmd *cobra.Command, args []string) error {
		command, err := readNewCommand(args)
		if err != nil {
			return err
		}
		p, err := store.Add(database.NewPin{
			Command:   command,
			Label:     addFlags.label,
			Tags:      addFlags.tags,
			Sensitive: addFlags.sensitive,
			Local:     addFlags.local,
		})
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(os.Stderr, "Pinned #%d: %s\n", p.ID, pinText(p))
		autoSync(writeSyncTimeout)
		return nil
	},
}

func readNewCommand(args []string) (string, error) {
	sources := 0
	for _, used := range []bool{len(args) > 0, addEdit, addLast} {
		if used {
			sources++
		}
	}
	if sources > 1 {
		return "", errors.New("use only one of: command arguments, --edit, --last")
	}
	switch {
	case len(args) > 0:
		return strings.Join(args, " "), nil
	case addEdit:
		return editor.Edit("")
	case addLast:
		return lastHistoryCommand()
	case !term.IsTerminal(int(os.Stdin.Fd())):
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("error reading input: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	default:
		prompt := promptui.Prompt{Label: "Enter the command you want to pin", Stdout: os.Stderr}
		return prompt.Run()
	}
}

func lastHistoryCommand() (string, error) {
	path, err := history.MostRecentFile(history.Candidates())
	if err != nil {
		return "", err
	}
	cmd, err := history.Last(path, func(c string) bool {
		return strings.HasPrefix(c, "pinner") || strings.HasPrefix(c, "pin-last")
	})
	if err != nil {
		return "", fmt.Errorf("%w in %s", err, path)
	}
	_, _ = fmt.Fprintf(os.Stderr, "Last command from %s:\n  %s\n", path, cmd)
	_, _ = fmt.Fprintln(os.Stderr, "(bash only writes history when a shell exits; \"pinner init bash\" sets up pin-last, which is always current)")
	return cmd, nil
}

func addPinFlags(c *cobra.Command, f *pinFlags) {
	c.Flags().StringVarP(&f.label, "label", "n", "", "a short name shown in the menu and matched by search")
	c.Flags().StringSliceVarP(&f.tags, "tag", "t", nil, "tags, comma-separated or repeated (filter with pinner -t <tag>)")
	c.Flags().BoolVar(&f.sensitive, "sensitive", false, "mask the command in menus and lists (e.g. it contains a token)")
	c.Flags().BoolVar(&f.local, "local", false, "never sync this pin to other devices")
}

func init() {
	addPinFlags(addCmd, &addFlags)
	addCmd.Flags().BoolVarP(&addEdit, "edit", "e", false, "write the command in $EDITOR (good for multi-line commands)")
	addCmd.Flags().BoolVar(&addLast, "last", false, "pin the last command from your shell history")
	// Everything after the first non-flag argument belongs to the pinned command: pinner add ls -la
	addCmd.Flags().SetInterspersed(false)
	rootCmd.AddCommand(addCmd)
}
