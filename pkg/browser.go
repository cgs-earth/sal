package pkg

import (
	"os"
	"os/exec"
	"runtime"
)

// HeadlessReason reports why no browser is likely to open for this session,
// or "" when one is: a CI environment, an SSH session, standard output that
// is not a terminal, or a Linux or BSD session with no display. Commands that
// would open a page, or ask a question whose answer only matters to someone
// at a terminal, use it to fall back to the prompt or to a default instead.
// Tests replace it.
var HeadlessReason = func() string {
	if os.Getenv("CI") != "" {
		return "CI is set"
	}
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return "this is an SSH session"
	}
	if info, err := os.Stdout.Stat(); err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return "standard output is not a terminal"
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return "there is no display"
	}
	return ""
}

// OpenBrowser opens url in the user's browser; tests replace it.
var OpenBrowser = func(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
