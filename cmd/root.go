package cmd

import (
	"database/sql"
	"github.com/charmbracelet/log"
	"github.com/manifoldco/promptui"
	"github.com/osean-man/pinner/internal/database"
	"github.com/spf13/cobra"
	"os"
	"os/exec"
	"strings"
)

type Pin struct {
	ID      int    `json:"id"`
	Command string `json:"command"`
}

var db *sql.DB

var rootCmd = &cobra.Command{
	Use:   "pinner",
	Short: "Manage your frequently used commands",
	Run: func(cmd *cobra.Command, args []string) {
		showDefaultMenu()
	},
}

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		log.Error(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	var err error
	db, err = database.InitializeDB()
	if err != nil {
		log.Error(os.Stderr, err)
		os.Exit(1)
	}
}

func showDefaultMenu() {
	pins, err := database.GetPins(db)
	if err != nil {
		log.Error(os.Stderr, "Error fetching pins:", err)
		os.Exit(1)
	}

	if len(pins) == 0 {
		log.Warn("You haven't added any pins yet.")
		prompt := promptui.Prompt{
			Label:     "Do you want to add one now?",
			IsConfirm: true,
		}

		result, err := prompt.Run()
		if err != nil {
			log.Errorf("Error getting input: %v", err)
			return
		}

		if result == "y" || result == "Y" {
			prompt := promptui.Prompt{
				Label: "Enter the command to add",
			}

			newCommand, err := prompt.Run()
			if err != nil {
				log.Errorf("Error getting input: %v", err)
				return
			}

			err = database.AddPin(db, newCommand)
			if err != nil {
				log.Errorf("Error adding pin: %v", err)
				return
			}

			log.Infof("Command added successfully!")
		}
		return
	}

	items := make([]Pin, len(pins))
	for i, p := range pins {
		items[i] = Pin{ID: p.ID, Command: p.Command}
	}

	templates := &promptui.SelectTemplates{
		Label:    "{{ . }}",
		Active:   "📌 {{ .Command | green }}",
		Inactive: "  {{ .Command }}",
		Selected: "📍 {{ .Command | red | cyan }}",
	}

	searcher := func(input string, index int) bool {
		pin := items[index]
		return strings.Contains(strings.ToLower(pin.Command), strings.ToLower(input))
	}

	prompt := promptui.Select{
		Label:     "Select a pinned command: ",
		Items:     items,
		Templates: templates,
		Size:      10,
		Searcher:  searcher,
	}

	index, _, err := prompt.Run()
	if err != nil {
		log.Errorf("Error selecting command: %v", err)
		return
	}

	selectedCommandID := pins[index].ID
	selectedCommand, err := database.GetPinByID(db, selectedCommandID)
	if err != nil {
		log.Errorf("Error fetching command: %v", err)
		return
	}

	cmdArgs := strings.Fields(selectedCommand)
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		log.Errorf("Error executing command: %v", err)
	}
}
