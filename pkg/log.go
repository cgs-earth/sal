package pkg

import (
	"fmt"
	"log/slog"
	"strings"
)

// ConfirmPrompt, when set, answers Confirm's question in place of the
// terminal. The progress panes of `sal build` set it while they own the
// terminal, since a prompt read from stdin would never be seen under them.
var ConfirmPrompt func(prompt string) bool

// Confirm asks the user a yes/no question, on the terminal unless
// ConfirmPrompt is set, and reports whether they answered yes.
func Confirm(prompt string) bool {

	Warnf("%s [y/N]", prompt)

	if ConfirmPrompt != nil {
		return ConfirmPrompt(prompt)
	}

	var input string
	if _, err := fmt.Scanln(&input); err != nil {
		// If user just presses enter, Scanln errors → treat as "no"
		return false
	}

	input = strings.ToLower(strings.TrimSpace(input))
	return input == "y" || input == "yes"
}

func Warnf(msg string, args ...any) {
	formattedMsg := fmt.Sprintf(msg, args...)
	slog.Warn(formattedMsg)
}

func Errorf(msg string, args ...any) {
	formattedMsg := fmt.Sprintf(msg, args...)
	slog.Error(formattedMsg)
}

func Infof(msg string, args ...any) {
	formattedMsg := fmt.Sprintf(msg, args...)
	slog.Info(formattedMsg)
}
