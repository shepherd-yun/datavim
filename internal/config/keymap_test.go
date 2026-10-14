package config

import (
	"strings"
	"testing"
)

func TestLoadKeymap(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "keymap.toml", `
leader = ","

[grid]
"S" = "sort.column"
"s" = ""

[global]
"<leader>f" = "search.global"

[popup]
"<ctrl+t>" = "structure.full"
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Keymap.Leader != "," {
		t.Fatalf("leader = %q", cfg.Keymap.Leader)
	}
	o := cfg.Keymap.Overrides()
	if o["grid"]["S"] != "sort.column" || o["grid"]["s"] != "" || o["global"]["<leader>f"] != "search.global" ||
		o["popup"]["<ctrl+t>"] != "structure.full" {
		t.Fatalf("overrides = %v", o)
	}
	if _, ok := o["tree"]; ok {
		t.Fatal("tables missing from the file are left out")
	}
}

func TestLoadKeymapErrors(t *testing.T) {
	for name, body := range map[string]string{
		"unknown table": "[gird]\n\"S\" = \"sort.column\"\n",
		"not a string":  "[grid]\n\"S\" = 1\n",
		"bad toml":      "leader = \n",
	} {
		dir := t.TempDir()
		writeFile(t, dir, "keymap.toml", body)
		_, err := Load(dir)
		if err == nil || !strings.Contains(err.Error(), "keymap.toml:") {
			t.Errorf("%s: err = %v, want it to name keymap.toml", name, err)
		}
	}
}

func TestLoadTheme(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.toml", "page_size = 50\n\n[theme]\nfocus = \"#ff8800\"\n")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Settings.PageSize != 50 || cfg.Theme["focus"] != "#ff8800" {
		t.Fatalf("settings = %+v, theme = %v", cfg.Settings, cfg.Theme)
	}
}
