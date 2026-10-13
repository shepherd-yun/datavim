// Package clipboard copies text to and reads text from the system clipboard.
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
		if useMacTool("pbcopy", runtime.GOOS, os.Getenv, exec.LookPath) {
			cmd := exec.Command("pbcopy")
			cmd.Stdin = strings.NewReader(text)
			if err := cmd.Run(); err == nil {
				return nil
			}
		}
		return tea.SetClipboard(text)()
	}
}

// Read reads the clipboard: pbpaste in a local macOS session, whose text
// done turns into a message; otherwise it asks the terminal over OSC 52,
// and the terminal's answer (if it allows reading) arrives as a
// tea.ClipboardMsg.
func Read(done func(text string) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		if useMacTool("pbpaste", runtime.GOOS, os.Getenv, exec.LookPath) {
			if out, err := exec.Command("pbpaste").Output(); err == nil {
				return done(string(out))
			}
		}
		return tea.ReadClipboard()
	}
}

// useMacTool reports whether the macOS clipboard tool works here: a local
// (not SSH) macOS session that has it.
func useMacTool(tool, goos string, getenv func(string) string, lookPath func(string) (string, error)) bool {
	if goos != "darwin" || getenv("SSH_TTY") != "" {
		return false
	}
	_, err := lookPath(tool)
	return err == nil
}
