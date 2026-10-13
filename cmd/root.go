package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/manifoldco/promptui"
	"github.com/osean-man/pinner/internal/config"
	"github.com/osean-man/pinner/internal/database"
	"github.com/osean-man/pinner/internal/placeholder"
	"github.com/spf13/cobra"
)

var (
	store       *database.Store
	copyFlag    bool
	printFlag   bool
	confirmFlag bool
	tagFlag     string
)

// exitError carries the exit status of a command pinner ran, so pinner can exit with it.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

var rootCmd = &cobra.Command{
	Use:   "pinner",
	Short: "Manage your frequently used commands",
	Long: `Pinner keeps a list of commands you pin and lets you pick one to run, copy or print.

Pins can have labels and tags, placeholders like <<host>> or <<port:8080>> that are filled in
when you run them, and can be synced end-to-end encrypted between your devices (see "pinner sync").`,
	SilenceUsage:      true,
	SilenceErrors:     true,
	PersistentPreRunE: openStore,
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		if store != nil {
			_ = store.Close()
		}
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return showDefaultMenu()
	},
}

// Execute runs the CLI.
func Execute() {
	err := rootCmd.Execute()
	if store != nil {
		_ = store.Close()
	}
	var exit exitError
	switch {
	case errors.As(err, &exit):
		os.Exit(exit.code)
	case errors.Is(err, promptui.ErrInterrupt), errors.Is(err, promptui.ErrEOF):
		os.Exit(130)
	case err != nil:
		_, _ = fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.Flags().BoolVarP(&copyFlag, "copy", "c", false, "copy the command to your clipboard instead of running it")
	rootCmd.Flags().BoolVarP(&printFlag, "print", "p", false, "print the command to stdout instead of running it (for shell widgets and eval)")
	rootCmd.Flags().BoolVar(&confirmFlag, "confirm", os.Getenv("PINNER_CONFIRM") == "1", "show the final command and ask before running it (env PINNER_CONFIRM=1)")
	rootCmd.Flags().StringVarP(&tagFlag, "tag", "t", "", "only show pins with this tag")
	rootCmd.MarkFlagsMutuallyExclusive("copy", "print")
}

// openStore opens the pins database for commands that need it.
func openStore(cmd *cobra.Command, args []string) error {
	if cmd == initCmd {
		return nil
	}
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "completion", "help", "__complete":
			return nil
		}
	}
	path, err := config.DBPath()
	if err != nil {
		return err
	}
	store, err = database.Open(path)
	return err
}

func showDefaultMenu() error {
	autoSync(menuSyncTimeout)
	pins, err := store.List(tagFlag)
	if err != nil {
		return err
	}

	if len(pins) == 0 {
		if tagFlag != "" {
			return fmt.Errorf("no pins tagged %q", tagFlag)
		}
		return offerFirstPin()
	}

	pin, err := selectPin(pins, "Select a pinned command", "📍")
	if err != nil {
		return err
	}

	command, err := fillPlaceholders(pin.Command)
	if err != nil {
		return err
	}
	if confirmFlag && !printFlag && !copyFlag {
		ok, err := confirm(fmt.Sprintf("Run %s", displayCommand(command, terminalWidth()-20)))
		if err != nil || !ok {
			return err
		}
	}
	if err := store.RecordUse(pin.ID); err != nil {
		warn("could not record usage: %v", err)
	}

	switch {
	case printFlag:
		fmt.Println(command)
		return nil
	case copyFlag:
		if err := clipboard.WriteAll(command); err != nil {
			return fmt.Errorf("error copying command to clipboard: %w", err)
		}
		_, _ = fmt.Fprintln(os.Stderr, "Command copied to clipboard!")
		return nil
	}
	return runShell(command)
}

func offerFirstPin() error {
	_, _ = fmt.Fprintln(os.Stderr, "You haven't added any pins yet.")
	ok, err := confirm("Do you want to add one now")
	if err != nil || !ok {
		return err
	}
	prompt := promptui.Prompt{Label: "Enter the command to add", Stdout: os.Stderr}
	command, err := prompt.Run()
	if err != nil {
		return err
	}
	if _, err := store.Add(database.NewPin{Command: command}); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(os.Stderr, "Command pinned successfully!")
	autoSync(writeSyncTimeout)
	return nil
}

// fillPlaceholders prompts for each <<name>> placeholder in command.
func fillPlaceholders(command string) (string, error) {
	params := placeholder.Find(command)
	if len(params) == 0 {
		return command, nil
	}
	values := make(map[string]string, len(params))
	for _, p := range params {
		label := p.Name
		if p.Default != "" {
			label = fmt.Sprintf("%s [%s]", p.Name, p.Default)
		}
		prompt := promptui.Prompt{Label: label, Stdout: os.Stderr}
		v, err := prompt.Run()
		if err != nil {
			return "", err
		}
		if v == "" {
			v = p.Default
		}
		values[p.Name] = v
	}
	return placeholder.Fill(command, values), nil
}

func confirm(label string) (bool, error) {
	prompt := promptui.Prompt{Label: label, IsConfirm: true, Stdout: os.Stderr}
	_, err := prompt.Run()
	if errors.Is(err, promptui.ErrAbort) {
		return false, nil
	}
	return err == nil, err
}

func runShell(command string) error {
	userShell := os.Getenv("SHELL")
	if userShell == "" {
		userShell = "sh"
	}
	c := exec.Command(userShell, "-c", command)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := c.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exitError{code: exit.ExitCode()}
	}
	return err
}

func warn(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "pinner: "+strings.TrimSuffix(format, "\n")+"\n", args...)
}
