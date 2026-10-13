package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/manifoldco/promptui"
	"github.com/osean-man/pinner/internal/database"
	"github.com/osean-man/pinner/internal/placeholder"
)

// envAskSave set to "0" turns off the "save these values?" question; values are still remembered.
const envAskSave = "PINNER_ASK_SAVE"

const (
	actionRun         = "Run"
	actionSaveDefault = "Run and save as defaults"
	actionNewPin      = "Save as new pin, then run"
)

// fillPlaceholders prompts for each <<name>> placeholder in the pin. Each prompt is pre-filled with
// the value used last time on this device (or the pin's default), so Enter repeats the last run.
// If any value is new, it offers to save the values as the pin's defaults or as a new pin.
// It returns the command to run and the pin the run belongs to.
func fillPlaceholders(pin database.Pin) (string, database.Pin, error) {
	params := placeholder.Find(pin.Command)
	if len(params) == 0 {
		return pin.Command, pin, nil
	}

	// Sensitive pins don't remember values: they are often tokens.
	remembered := map[string]string{}
	if !pin.Sensitive {
		var err error
		if remembered, err = store.LastValues(pin.ID); err != nil {
			warn("could not load last values: %v", err)
			remembered = map[string]string{}
		}
	}

	values := make(map[string]string, len(params))
	changed := false
	for _, p := range params {
		prefill, ok := remembered[p.Name]
		if !ok {
			prefill = p.Default
		}
		label := p.Name
		switch {
		case prefill == "":
		case pin.Sensitive:
			label += " [default]" // don't print part of a sensitive command
		default:
			label += " [" + prefill + "]"
		}
		v, err := (&promptui.Prompt{Label: label, Stdout: os.Stderr}).Run()
		if err != nil {
			return "", pin, err
		}
		if v == "" {
			v = prefill
		}
		if v != prefill {
			changed = true
		}
		values[p.Name] = v
	}

	if !pin.Sensitive {
		if err := store.SaveLastValues(pin.ID, values); err != nil {
			warn("could not remember values: %v", err)
		}
	}
	filled := placeholder.Fill(pin.Command, values)
	if !changed || os.Getenv(envAskSave) == "0" {
		return filled, pin, nil
	}

	action, err := chooseSaveAction()
	if err != nil {
		return "", pin, err
	}
	switch action {
	case actionSaveDefault:
		updated, err := saveDefaults(pin, values)
		if err != nil {
			warn("defaults not saved: %v", err)
			return filled, pin, nil
		}
		_, _ = fmt.Fprintf(os.Stderr, "Saved defaults for #%d.\n", updated.ID)
		// Sync now: the command may run for a long time (or forever, like an ssh tunnel).
		autoSync(writeSyncTimeout)
		return filled, updated, nil
	case actionNewPin:
		created, err := saveAsNewPin(pin, filled)
		if err != nil {
			warn("new pin not saved: %v", err)
			return filled, pin, nil
		}
		_, _ = fmt.Fprintf(os.Stderr, "Pinned #%d: %s\n", created.ID, pinText(created))
		autoSync(writeSyncTimeout)
		return filled, created, nil
	}
	return filled, pin, nil
}

func chooseSaveAction() (string, error) {
	sel := promptui.Select{
		Label:    "You entered new values",
		Items:    []string{actionRun, actionSaveDefault, actionNewPin},
		Stdout:   os.Stderr,
		HideHelp: true,
	}
	_, action, err := sel.Run()
	return action, err
}

func saveDefaults(pin database.Pin, values map[string]string) (database.Pin, error) {
	command, err := placeholder.SetDefaults(pin.Command, values)
	if err != nil {
		return pin, err
	}
	return store.Update(pin.ID, database.PinUpdate{Command: &command})
}

func saveAsNewPin(pin database.Pin, filled string) (database.Pin, error) {
	label, err := (&promptui.Prompt{Label: "Label for the new pin", Default: pin.Label, AllowEdit: true, Stdout: os.Stderr}).Run()
	if err != nil {
		return pin, err
	}
	created, err := store.Add(database.NewPin{Command: filled, Label: label, Tags: pin.Tags, Sensitive: pin.Sensitive, Local: pin.Local})
	if errors.Is(err, database.ErrDuplicate) {
		return pin, errors.New("a pin with exactly that command already exists")
	}
	return created, err
}
