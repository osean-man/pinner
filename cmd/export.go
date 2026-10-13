package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/osean-man/pinner/internal/database"
	"github.com/spf13/cobra"
)

// exportFile is the JSON format used by export and import.
type exportFile struct {
	Version    int         `json:"version"`
	ExportedAt time.Time   `json:"exported_at"`
	Pins       []exportPin `json:"pins"`
}

type exportPin struct {
	Command   string   `json:"command"`
	Label     string   `json:"label,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Sensitive bool     `json:"sensitive,omitempty"`
	Local     bool     `json:"local,omitempty"`
}

var exportOutput string

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export pins as JSON (to stdout, or a file with -o)",
	Long:  "Export pins as JSON. The export is not encrypted and includes sensitive pins in full.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		pins, err := store.List("")
		if err != nil {
			return err
		}
		out := exportFile{Version: 1, ExportedAt: time.Now().UTC(), Pins: []exportPin{}}
		for _, p := range pins {
			out.Pins = append(out.Pins, exportPin{Command: p.Command, Label: p.Label, Tags: p.Tags, Sensitive: p.Sensitive, Local: p.Local})
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false) // keep <<placeholders>> and shell operators readable
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return err
		}
		b := buf.Bytes()
		if exportOutput == "" || exportOutput == "-" {
			_, err = os.Stdout.Write(b)
			return err
		}
		if err := os.WriteFile(exportOutput, b, 0o600); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(os.Stderr, "Exported %d pins to %s\n", len(out.Pins), exportOutput)
		return nil
	},
}

var importCmd = &cobra.Command{
	Use:   "import <file|->",
	Short: "Import pins from a JSON export (pins that already exist are skipped)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var r io.Reader = os.Stdin
		if args[0] != "-" {
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			r = f
		}
		var in exportFile
		if err := json.NewDecoder(r).Decode(&in); err != nil {
			return fmt.Errorf("not a pinner export: %w", err)
		}
		if in.Version != 1 {
			return fmt.Errorf("unsupported export version %d", in.Version)
		}
		added, skipped := 0, 0
		for _, p := range in.Pins {
			_, err := store.Add(database.NewPin{Command: p.Command, Label: p.Label, Tags: p.Tags, Sensitive: p.Sensitive, Local: p.Local})
			switch {
			case errors.Is(err, database.ErrDuplicate), errors.Is(err, database.ErrEmpty):
				skipped++
			case err != nil:
				return err
			default:
				added++
			}
		}
		_, _ = fmt.Fprintf(os.Stderr, "Imported %d pins (%d skipped as duplicates or empty).\n", added, skipped)
		if added > 0 {
			autoSync(writeSyncTimeout)
		}
		return nil
	},
}

func init() {
	exportCmd.Flags().StringVarP(&exportOutput, "output", "o", "", "write to this file (mode 0600) instead of stdout")
	rootCmd.AddCommand(exportCmd, importCmd)
}
