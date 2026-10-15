package ui

import (
	"strings"
	"testing"
)

func TestWorkspaceIsShown(t *testing.T) {
	m, _ := newTestModel(t)
	m.SetWorkspace("personal")
	s := screen(m)
	lines := strings.Split(s, "\n")
	if status := lines[len(lines)-1]; !strings.Contains(status, "[personal]") {
		t.Fatalf("status bar must name the workspace:\n%s", status)
	}
	if !strings.Contains(s, "sqlvim — personal") {
		t.Fatalf("welcome box title must name the workspace:\n%s", s)
	}
	if got := m.View().WindowTitle; got != "sqlvim — personal" {
		t.Fatalf("WindowTitle = %q", got)
	}
}

func TestDefaultWorkspaceIsNotShown(t *testing.T) {
	for _, name := range []string{"", "default"} {
		m, _ := newTestModel(t)
		m.SetWorkspace(name)
		s := screen(m)
		if strings.Contains(s, "[default]") || strings.Contains(s, "sqlvim — ") {
			t.Fatalf("workspace %q must not be shown:\n%s", name, s)
		}
		if got := m.View().WindowTitle; got != "" {
			t.Fatalf("workspace %q: WindowTitle = %q", name, got)
		}
	}
}
