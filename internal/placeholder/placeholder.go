// Package placeholder finds and fills <<name>> and <<name:default>> placeholders in commands.
//
// The <<...>> syntax was chosen because {{...}} is common in real commands (docker --format,
// kubectl go-templates, helm) and <...> collides with shell redirection.
package placeholder

import (
	"regexp"
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
