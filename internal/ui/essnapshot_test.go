package ui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

func fakeSnapshots(f *fakeES) {
	f.repos = []es.Repository{
		{Name: "repo1", Type: "fs", Settings: map[string]string{"location": "/mnt/backups"}},
		{Name: "repo2", Type: "s3", Settings: map[string]string{"bucket": "b"}},
	}
	f.snaps = map[string][]*fakeSnap{
		"repo1": {
			{name: "s2", state: "IN_PROGRESS", indices: []string{"orders", "users"}, size: 4 << 30, done: 1288490189},
			{name: "s1", state: "SUCCESS", indices: []string{"orders", "users"}, size: 6024},
		},
		"repo2": {},
	}
}

func repoNode(t *testing.T, m *Model, repo string) *tree.Node {
	t.Helper()
	sec := esSection(t, m, esSecSnapshots)
	sec.Expanded = true
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindESRepo && n.Key == repo })
	return m.tree.Selected()
}

func TestESTreeSnapshots(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fakeSnapshots(f)
	pressAll(t, m, "l")
	sec := esSection(t, m, esSecSnapshots)
	if sec.Name != "Snapshots (2)" {
		t.Fatalf("section = %q", sec.Name)
	}
	if c := sec.Children[0]; c.Key != "repo1" || c.Detail != "fs · /mnt/backups" || sec.Children[1].Detail != "s3" {
		t.Errorf("repos = %+v %+v", c, sec.Children[1])
	}
	if got := sentContaining(f, "_cat/snapshots"); len(got) != 0 {
		t.Fatalf("the tree load read snapshots: %v", got)
	}
	n := repoNode(t, m, "repo1")
	pressAll(t, m, "l")
	if got := sentContaining(f, "_cat/snapshots"); len(got) != 1 || !strings.Contains(got[0], "_cat/snapshots/repo1") {
		t.Fatalf("sent %v", got)
	}
	if len(n.Children) != 2 {
		t.Fatalf("children %v", names(n.Children))
	}
	s2, s1 := n.Children[0], n.Children[1]
	if s2.Kind != tree.KindESSnapshot || s2.Key != "repo1/s2" || !s2.Warn || !strings.HasPrefix(s2.Detail, "IN_PROGRESS · ") ||
		!strings.HasSuffix(s1.Detail, " · 12s · 2 indices") || s1.Warn {
		t.Errorf("snapshots %+v %+v", s2, s1)
	}
}

func TestESTreeNoRepos(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", "", 1)
	pressAll(t, m, "l")
	for _, c := range m.tree.Roots()[0].Children {
		if c.Section == esSecSnapshots {
			t.Errorf("snapshots section without repositories: %+v", c)
		}
	}
	f.routes["GET _snapshot"] = func(es.Request) es.Response { return errResp(403, "security_exception", "no") }
	f.repos = []es.Repository{{Name: "r", Type: "fs"}}
	pressAll(t, m, "space", "r")
	for _, c := range m.tree.Roots()[0].Children {
		if c.Section == esSecSnapshots {
			t.Errorf("snapshots section on 403: %+v", c)
		}
	}
}

func TestESTreeRepoError(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fakeSnapshots(f)
	f.routes["GET _cat/snapshots/repo2"] = func(es.Request) es.Response {
		return errResp(500, "repository_exception", "[repo2] could not read repository data")
	}
	pressAll(t, m, "l")
	r2 := repoNode(t, m, "repo2")
	pressAll(t, m, "l")
	if !strings.Contains(r2.Err, "could not read repository data") {
		t.Errorf("repo2 err %q", r2.Err)
	}
	r1 := repoNode(t, m, "repo1")
	pressAll(t, m, "l")
	if r1.Err != "" || len(r1.Children) != 2 {
		t.Errorf("repo1 %q %v", r1.Err, names(r1.Children))
	}
	if idx := esSection(t, m, esSecIndices); idx.Err != "" {
		t.Errorf("indices %q", idx.Err)
	}
}

func openSnapshotTab(t *testing.T, m *Model, repo, name string) *esSnapshotTab {
	t.Helper()
	repoNode(t, m, repo)
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindESSnapshot && n.Key == repo+"/"+name })
	pressAll(t, m, "enter")
	tab, ok := m.activeTab().(*esSnapshotTab)
	if !ok {
		t.Fatalf("active tab = %T (status %q)", m.activeTab(), m.status)
	}
	return tab
}

func TestESSnapshotTab(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fakeSnapshots(f)
	pressAll(t, m, "l")
	tab := openSnapshotTab(t, m, "repo1", "s1")
	if tab.Title() != "s1 · snapshot" {
		t.Errorf("title %q", tab.Title())
	}
	s := screen(m)
	for _, want := range []string{"SUCCESS", "5.9 KB", "orders", "users", "global state"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}

	m.setFocus(focusTree)
	tab = openSnapshotTab(t, m, "repo1", "s2")
	if s := screen(m); !strings.Contains(s, "1.2 GB / 4.0 GB (30%)") {
		t.Errorf("progress:\n%s", s)
	}
	n := len(sentContaining(f, "_snapshot/repo1/s2"))
	id := tab.id
	drain(t, m, func() tea.Msg { return esSnapshotTickMsg{tabID: id, gen: tab.gen} })
	if got := len(sentContaining(f, "_snapshot/repo1/s2")); got <= n {
		t.Fatalf("IN_PROGRESS not reread: %d", got)
	}
	f.snap("repo1", "s2").state = "SUCCESS"
	drain(t, m, func() tea.Msg { return esSnapshotTickMsg{tabID: id, gen: tab.gen} })
	if tab.ticking() {
		t.Error("still ticking after SUCCESS")
	}
	f.snap("repo1", "s2").state = "IN_PROGRESS"
	pressAll(t, m, "space", "r")
	if !tab.ticking() {
		t.Error("not ticking while IN_PROGRESS")
	}
	tab.Hidden()
	if tab.ticking() {
		t.Error("ticking while hidden")
	}
}

func TestESCreateSnapshot(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fakeSnapshots(f)
	pressAll(t, m, "l")
	repoNode(t, m, "repo1")
	pressAll(t, m, "l", "o")
	if _, ok := m.overlay.(*overlay.SnapshotForm); !ok {
		t.Fatalf("overlay %T status %q", m.overlay, m.status)
	}
	drain(t, m, func() tea.Msg { return overlay.SnapshotFormMsg{Repo: "repo1", Name: "snap-x", Indices: []string{"orders"}} })
	if v := confirmText(t, m); !strings.Contains(v, "snapshot orders into repo1 as snap-x") {
		t.Fatalf("confirm:\n%s", v)
	}
	f.snaps["repo1"] = append(f.snaps["repo1"], &fakeSnap{name: "snap-x", state: "IN_PROGRESS", indices: []string{"orders"}, size: 10, done: 1})
	pressAll(t, m, "y")
	if !slices.Contains(f.sent(), "PUT _snapshot/repo1/snap-x?wait_for_completion=false") ||
		!sameJSONText(f.lastBody("PUT _snapshot/repo1/snap-x"), `{"indices":"orders","include_global_state":false}`) {
		t.Fatalf("sent %v", f.sent())
	}
	if tab, ok := m.activeTab().(*esSnapshotTab); !ok || tab.name != "snap-x" {
		t.Errorf("active tab %T", m.activeTab())
	}
	if n := len(sentContaining(f, "_cat/snapshots/repo1")); n < 2 {
		t.Errorf("repo not reread")
	}
}

func TestESCreateRepository(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", "", 1)
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindConn })
	pressAll(t, m, "a")
	menuPick(t, m, "New snapshot repository…")
	typeText(t, m, "backups")
	pressAll(t, m, "enter")
	path := m.esRepoEdit.path
	if path == "" {
		t.Fatalf("no temp file; status %q", m.status)
	}
	if err := writeFile(path, `{"type":"fs","settings":{"location":"/snapshots"}}`); err != nil {
		t.Fatal(err)
	}
	drain(t, m, func() tea.Msg { return esRepoEditDoneMsg{conn: "search", name: "backups", path: path} })
	if v := confirmText(t, m); !strings.Contains(v, "register snapshot repository backups") {
		t.Fatalf("confirm:\n%s", v)
	}
	typeText(t, m, "backups")
	pressAll(t, m, "enter")
	if b := f.lastBody("PUT _snapshot/backups"); !sameJSONText(b, `{"type":"fs","settings":{"location":"/snapshots"}}`) {
		t.Fatalf("body %q sent %v", b, f.sent())
	}
}

func TestESDeleteSnapshot(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fakeSnapshots(f)
	pressAll(t, m, "l")
	st := openSnapshotTab(t, m, "repo1", "s1")
	m.setFocus(focusTree)
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindESSnapshot && n.Key == "repo1/s1" })
	pressAll(t, m, "d", "d")
	if v := confirmText(t, m); !strings.Contains(v, "delete snapshot s1 from repo1") {
		t.Fatalf("confirm:\n%s", v)
	}
	typeText(t, m, "s1")
	pressAll(t, m, "enter")
	if !slices.Contains(f.sent(), "DELETE _snapshot/repo1/s1") {
		t.Fatalf("sent %v", f.sent())
	}
	if slices.Contains(m.tabs, tab(st)) {
		t.Error("the snapshot's tab is still open")
	}

	repoNode(t, m, "repo2")
	pressAll(t, m, "d", "d")
	if v := confirmText(t, m); !strings.Contains(v, "unregister repository repo2 (the snapshot files stay in the storage)") {
		t.Fatalf("confirm:\n%s", v)
	}
	typeText(t, m, "repo2")
	pressAll(t, m, "enter")
	if !slices.Contains(f.sent(), "DELETE _snapshot/repo2") {
		t.Fatalf("sent %v", f.sent())
	}
}

func TestESSnapshotTabDelete(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fakeSnapshots(f)
	pressAll(t, m, "l")
	openSnapshotTab(t, m, "repo1", "s1")
	pressAll(t, m, "d", "d")
	typeText(t, m, "s1")
	pressAll(t, m, "enter")
	if !slices.Contains(f.sent(), "DELETE _snapshot/repo1/s1") {
		t.Fatalf("sent %v", f.sent())
	}
}

func TestESSnapshotReadOnly(t *testing.T) {
	ro := true
	m, _, f := newESModel(t, config.Connection{ReadOnly: &ro})
	fakeSnapshots(f)
	pressAll(t, m, "l")
	repoNode(t, m, "repo1")
	pressAll(t, m, "o")
	if m.overlay != nil || m.status != readOnlyHint {
		t.Errorf("o: overlay %T status %q", m.overlay, m.status)
	}
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindESSnapshot && n.Key == "repo1/s1" })
	pressAll(t, m, "d", "d")
	if m.overlay != nil || m.status != readOnlyHint {
		t.Errorf("dd: overlay %T status %q", m.overlay, m.status)
	}
	for _, s := range f.sent() {
		if !strings.HasPrefix(s, "GET") {
			t.Errorf("sent %s", s)
		}
	}
}

// menuPick opens the node menu (already opened by "a") entry named desc.
func menuPick(t *testing.T, m *Model, desc string) {
	t.Helper()
	at := slices.IndexFunc(m.nodeMenu(m.tree.Selected()), func(it menuItem) bool { return it.desc == desc })
	if at < 0 {
		t.Fatalf("no %q in %q", desc, menuDescs(m))
	}
	for range at {
		press(t, m, "j")
	}
	press(t, m, "enter")
}
