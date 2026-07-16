package pkg

import (
	"fmt"
	"log/slog"
	"strings"
)

func Confirm(prompt string) bool {

	Warnf("%s [y/N]", prompt)

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
