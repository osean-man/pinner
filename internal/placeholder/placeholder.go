// Package placeholder finds and fills <<name>> and <<name:default>> placeholders in commands.
//
// The <<...>> syntax was chosen because {{...}} is common in real commands (docker --format,
// kubectl go-templates, helm) and <...> collides with shell redirection.
package placeholder

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var pattern = regexp.MustCompile(`<<([A-Za-z_][A-Za-z0-9_-]*)(?::([^<>\n]*))?>>`)

// Param is one placeholder in a command.
type Param struct {
	Name    string
	Default string
}

// Find returns the unique placeholders in command, in order of first appearance.
// When a name appears more than once, the first default given wins.
func Find(command string) []Param {
	var params []Param
	index := map[string]int{}
	for _, m := range pattern.FindAllStringSubmatch(command, -1) {
		name, def := m[1], m[2]
		if i, ok := index[name]; ok {
			if params[i].Default == "" {
				params[i].Default = def
			}
			continue
		}
		index[name] = len(params)
		params = append(params, Param{Name: name, Default: def})
	}
	return params
}

// Fill replaces every placeholder with its value. Placeholders missing from values use their default.
func Fill(command string, values map[string]string) string {
	defaults := map[string]string{}
	for _, p := range Find(command) {
		defaults[p.Name] = p.Default
	}
	return pattern.ReplaceAllStringFunc(command, func(m string) string {
		name := pattern.FindStringSubmatch(m)[1]
		if v, ok := values[name]; ok {
			return v
		}
		return defaults[name]
	})
}

// ErrUnstorableDefault is returned by SetDefaults for values that can't be written inside <<name:...>>.
var ErrUnstorableDefault = errors.New("value can't be saved as a default (it contains <, > or a newline)")

// CanBeDefault reports whether value can be stored as a placeholder default.
func CanBeDefault(value string) bool {
	return !strings.ContainsAny(value, "<>\n")
}

// SetDefaults rewrites every occurrence of the placeholders named in values to use that value as its
// default (an empty value removes the default). Other placeholders and text are left unchanged.
func SetDefaults(command string, values map[string]string) (string, error) {
	for name, v := range values {
		if !CanBeDefault(v) {
			return "", fmt.Errorf("%s: %w", name, ErrUnstorableDefault)
		}
	}
	return pattern.ReplaceAllStringFunc(command, func(m string) string {
		name := pattern.FindStringSubmatch(m)[1]
		v, ok := values[name]
		if !ok {
			return m
		}
		if v == "" {
			return "<<" + name + ">>"
		}
		return "<<" + name + ":" + v + ">>"
	}), nil
}
