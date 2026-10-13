package overlay

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestSecretMasksAndSubmits(t *testing.T) {
	type got struct{ v string }
	s := NewSecret("SSH key", "Passphrase for /k:", func(v string) tea.Msg { return got{v} })
	o, msg := send(t, s, "s", "3", "c")
	if o == nil || msg != nil {
		t.Fatal("still open while typing")
	}
	v := ansi.Strip(s.View(80, 20))
	if strings.Contains(v, "s3c") || !strings.Contains(v, "•••") || !strings.Contains(v, "Passphrase for /k:") {
		t.Fatalf("view:\n%s", v)
	}
	o, msg = send(t, s, "enter")
	if o != nil || msg != (got{"s3c"}) {
		t.Fatalf("o = %v, msg = %#v", o, msg)
	}
	if o, _ := send(t, NewSecret("t", "l", func(string) tea.Msg { return nil }), "esc"); o != nil {
		t.Fatal("esc cancels")
	}
}
