// Package clipboard copies text to the system clipboard.
package clipboard

import (
	"os"
	"os/exec"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Copy puts text on the clipboard: pbcopy in a local macOS session,
// otherwise the terminal's OSC 52 sequence (which also works over SSH).
func Copy(text string) tea.Cmd {
	return func() tea.Msg {
		if usePbcopy(runtime.GOOS, os.Getenv, exec.LookPath) {
			cmd := exec.Command("pbcopy")
			cmd.Stdin = strings.NewReader(text)
			if err := cmd.Run(); err == nil {
				return nil
			}
		}
		return tea.SetClipboard(text)()
	}
}

func usePbcopy(goos string, getenv func(string) string, lookPath func(string) (string, error)) bool {
	if goos != "darwin" || getenv("SSH_TTY") != "" {
		return false
	}
	_, err := lookPath("pbcopy")
	return err == nil
}
