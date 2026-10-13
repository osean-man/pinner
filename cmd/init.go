package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

const bashInit = `# pinner shell integration for bash. Add to ~/.bashrc:  eval "$(pinner init bash)"
# Alt+p: pick a pin and insert it at the cursor, so you can edit it before pressing Enter.
__pinner_widget() {
  local cmd
  cmd="$(pinner --print)" || return
  READLINE_LINE="${READLINE_LINE:0:READLINE_POINT}${cmd}${READLINE_LINE:READLINE_POINT}"
  READLINE_POINT=$((READLINE_POINT + ${#cmd}))
}
bind -m emacs-standard -x '"\ep": __pinner_widget'
bind -m vi-insert -x '"\ep": __pinner_widget'

# pin-last [pinner add flags]: pin the command you just ran.
pin-last() {
  local cmd
  cmd="$(HISTTIMEFORMAT= builtin fc -ln -2 -2)"
  cmd="${cmd#"${cmd%%[![:space:]]*}"}"
  pinner add "$@" -- "$cmd"
}
`

const zshInit = `# pinner shell integration for zsh. Add to ~/.zshrc:  eval "$(pinner init zsh)"
# Alt+p: pick a pin and insert it at the cursor, so you can edit it before pressing Enter.
__pinner_widget() {
  local cmd
  cmd="$(pinner --print </dev/tty)"
  if [[ -n "$cmd" ]]; then
    LBUFFER="${LBUFFER}${cmd}"
  fi
  zle reset-prompt
}
zle -N __pinner_widget
bindkey '^[p' __pinner_widget

# pin-last [pinner add flags]: pin the command you just ran.
pin-last() {
  pinner add "$@" -- "$(fc -ln -1)"
}
`

const fishInit = `# pinner shell integration for fish. Add to ~/.config/fish/config.fish:  pinner init fish | source
# Alt+p: pick a pin and insert it at the cursor, so you can edit it before pressing Enter.
function __pinner_widget
  set -l cmd (pinner --print | string collect)
  and commandline -i -- $cmd
  commandline -f repaint
end
bind \ep __pinner_widget
bind -M insert \ep __pinner_widget 2>/dev/null

# pin-last [pinner add flags]: pin the command you just ran.
function pin-last
  pinner add $argv -- $history[1]
end
`

var initCmd = &cobra.Command{
	Use:       "init <bash|zsh|fish>",
	Short:     "Print shell integration (Alt+p picker widget and pin-last)",
	Long:      "Print shell integration code: an Alt+p key binding that inserts a pin into your prompt, and a pin-last function that pins the previous command.",
	Example:   "  eval \"$(pinner init bash)\"   # in ~/.bashrc\n  eval \"$(pinner init zsh)\"    # in ~/.zshrc\n  pinner init fish | source     # in config.fish",
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"bash", "zsh", "fish"},
	RunE: func(cmd *cobra.Command, args []string) error {
		scripts := map[string]string{"bash": bashInit, "zsh": zshInit, "fish": fishInit}
		s, ok := scripts[args[0]]
		if !ok {
			return fmt.Errorf("unsupported shell %q (supported: bash, zsh, fish)", args[0])
		}
		fmt.Print(s)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
}
