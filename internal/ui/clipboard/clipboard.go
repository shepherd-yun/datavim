// Package clipboard copies text to and reads text from the system clipboard.
package clipboard

import (
	"encoding/base64"
	"os"
	"os/exec"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Copy puts text on the clipboard with the system's tool (pbcopy, the
// Windows clipboard from WSL, wl-copy, xclip or xsel) in a local session,
// otherwise with the terminal's OSC 52 sequence (which also works over
// SSH).
func Copy(text string) tea.Cmd {
	return func() tea.Msg {
		if tl := pick(runtime.GOOS, os.Getenv, exec.LookPath); tl != nil {
			cmd := exec.Command(tl.copy[0], tl.copy[1:]...)
			cmd.Stdin = strings.NewReader(tl.encode(text))
			if err := cmd.Run(); err == nil {
				return nil
			}
		}
		return tea.SetClipboard(text)()
	}
}

// Read reads the clipboard with the system's tool in a local session,
// whose text done turns into a message; otherwise it asks the terminal
// over OSC 52, and the terminal's answer (if it allows reading) arrives as
// a tea.ClipboardMsg.
func Read(done func(text string) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		if tl := pick(runtime.GOOS, os.Getenv, exec.LookPath); tl != nil {
			if out, err := exec.Command(tl.paste[0], tl.paste[1:]...).Output(); err == nil {
				if text, err := tl.decode(string(out)); err == nil {
					return done(text)
				}
			}
		}
		return tea.ReadClipboard()
	}
}

// tool is a clipboard command pair: copy reads the text on stdin, paste
// writes it on stdout.
type tool struct {
	copy, paste []string
	base64      bool // the text goes through base64, so no code page mangles it
}

// The WSL tool: powershell.exe on the Windows clipboard, the text in
// base64 both ways (stdin and stdout use the console's code page).
var wslTool = tool{
	copy: []string{
		"powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		"Set-Clipboard -Value ([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd().Trim())))",
	},
	paste: []string{
		"powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		"[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes([string](Get-Clipboard -Raw)))",
	},
	base64: true,
}

// pick is the clipboard tool of a local session (not SSH, whose tools
// would reach the server's clipboard), or nil for OSC 52.
func pick(goos string, getenv func(string) string, lookPath func(string) (string, error)) *tool {
	if getenv("SSH_TTY") != "" {
		return nil
	}
	has := func(tools ...string) bool {
		for _, t := range tools {
			if _, err := lookPath(t); err != nil {
				return false
			}
		}
		return true
	}
	candidates := []struct {
		ok bool
		tool
	}{
		{goos == "darwin" && has("pbcopy", "pbpaste"), tool{copy: []string{"pbcopy"}, paste: []string{"pbpaste"}}},
		{goos == "linux" && (getenv("WSL_DISTRO_NAME") != "" || getenv("WSL_INTEROP") != "") && has("powershell.exe"), wslTool},
		{
			goos == "linux" && getenv("WAYLAND_DISPLAY") != "" && has("wl-copy", "wl-paste"),
			tool{copy: []string{"wl-copy"}, paste: []string{"wl-paste", "--no-newline"}},
		},
		{
			goos == "linux" && getenv("DISPLAY") != "" && has("xclip"),
			tool{copy: []string{"xclip", "-selection", "clipboard", "-in"}, paste: []string{"xclip", "-selection", "clipboard", "-out"}},
		},
		{
			goos == "linux" && getenv("DISPLAY") != "" && has("xsel"),
			tool{copy: []string{"xsel", "--clipboard", "--input"}, paste: []string{"xsel", "--clipboard", "--output"}},
		},
	}
	for _, c := range candidates {
		if c.ok {
			return &c.tool
		}
	}
	return nil
}

// encode is text as the copy command reads it.
func (t *tool) encode(text string) string {
	if t.base64 {
		return base64.StdEncoding.EncodeToString([]byte(text))
	}
	return text
}

// decode is the paste command's output as text; the Windows clipboard's
// CRLF line ends become LF.
func (t *tool) decode(out string) (string, error) {
	if !t.base64 {
		return out, nil
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n"), nil
}
