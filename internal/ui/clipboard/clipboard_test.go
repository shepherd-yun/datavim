package clipboard

import (
	"errors"
	"slices"
	"testing"
)

func TestPick(t *testing.T) {
	has := func(tools ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			if slices.Contains(tools, name) {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		}
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	wsl := map[string]string{"WSL_DISTRO_NAME": "Ubuntu", "DISPLAY": ":0", "WAYLAND_DISPLAY": "wayland-0"}
	cases := []struct {
		name string
		goos string
		env  map[string]string
		look func(string) (string, error)
		want string // the copy command; "" is OSC 52
	}{
		{"mac", "darwin", nil, has("pbcopy", "pbpaste"), "pbcopy"},
		{"mac over ssh", "darwin", map[string]string{"SSH_TTY": "/dev/ttys001"}, has("pbcopy", "pbpaste"), ""},
		{"mac without the tools", "darwin", nil, has(), ""},
		{"wsl", "linux", wsl, has("powershell.exe", "wl-copy", "wl-paste"), "powershell.exe"},
		{"wsl without interop", "linux", wsl, has("wl-copy", "wl-paste"), "wl-copy"},
		{"wayland", "linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"}, has("wl-copy", "wl-paste", "xclip"), "wl-copy"},
		{"wayland without wl-clipboard", "linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"}, has("xclip"), "xclip"},
		{"x11 xclip", "linux", map[string]string{"DISPLAY": ":0"}, has("xclip", "xsel"), "xclip"},
		{"x11 xsel", "linux", map[string]string{"DISPLAY": ":0"}, has("xsel"), "xsel"},
		{"x11 over ssh", "linux", map[string]string{"DISPLAY": "localhost:10.0", "SSH_TTY": "/dev/pts/1"}, has("xclip"), ""},
		{"headless", "linux", nil, has("xclip", "wl-copy", "wl-paste"), ""},
		{"pbcopy on linux", "linux", nil, has("pbcopy", "pbpaste"), ""},
	}
	for _, c := range cases {
		got := ""
		if tl := pick(c.goos, env(c.env), c.look); tl != nil {
			got = tl.copy[0]
		}
		if got != c.want {
			t.Errorf("%s: copies with %q, want %q", c.name, got, c.want)
		}
	}
}

func TestWSLText(t *testing.T) {
	tl := pick("linux", func(k string) string { return map[string]string{"WSL_INTEROP": "/run/WSL/1_interop"}[k] },
		func(string) (string, error) { return "/mnt/c/powershell.exe", nil })
	in := "id\tname\n1\t张三\n"
	if got := tl.encode(in); got != "aWQJbmFtZQoxCeW8oOS4iQo=" {
		t.Fatalf("encode = %q", got)
	}
	// What powershell prints: the clipboard's CRLF text in base64, and a newline.
	if got, err := tl.decode("aWQJbmFtZQ0KMQnlvKDkuIkNCg==\r\n"); err != nil || got != in {
		t.Fatalf("decode = %q, %v", got, err)
	}
	plain := pick("darwin", func(string) string { return "" }, func(string) (string, error) { return "/usr/bin/x", nil })
	if plain.encode(in) != in {
		t.Fatal("other tools take the text as it is")
	}
	if got, _ := plain.decode(in); got != in {
		t.Fatal("other tools give the text as it is")
	}
}
