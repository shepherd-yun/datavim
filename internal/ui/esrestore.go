package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
)

// esRestore is the snapshot the restore form is for.
type esRestore struct {
	conn, repo, name string
	indices          []string
}

// esRestoreSnapMsg is the snapshot read before its restore form.
type esRestoreSnapMsg struct {
	esRestore
	err error
}

func (m esRestoreSnapMsg) failure() (string, error) { return m.conn, m.err }

// esRestorePlanMsg is a restore with the targets that exist.
type esRestorePlanMsg struct {
	conn, repo, name string
	spec             es.RestoreSpec
	src, targets     []string
	open, closed     []string // targets that exist
	err              error
}

func (m esRestorePlanMsg) failure() (string, error) { return m.conn, m.err }

// esRestoreRunMsg is a confirmed restore.
type esRestoreRunMsg struct {
	conn string
	spec es.RestoreSpec
}

// esRestoreDoneMsg is a restore started, or why not.
type esRestoreDoneMsg struct {
	conn string
	n    int
	err  error
}

func (m esRestoreDoneMsg) failure() (string, error) { return m.conn, m.err }

// askRestore reads the snapshot's indices for the restore form.
func (m *Model) askRestore(msg esRestoreAskMsg) tea.Cmd {
	if m.refuseReadOnly(msg.conn) {
		return nil
	}
	b := m.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		out := esRestoreSnapMsg{esRestore: esRestore{conn: msg.conn, repo: msg.repo, name: msg.name}}
		out.err = withES(ctx, b, msg.conn, func(d es.Doer) error {
			s, err := es.GetSnapshot(ctx, d, msg.repo, msg.name)
			out.indices = s.IndexNames
			return err
		})
		return out
	}
}

func (m *Model) esRestoreSnap(msg esRestoreSnapMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(esReason(msg.err), true)
		return nil
	}
	m.esRestore = msg.esRestore
	m.overlay = overlay.NewRestoreForm(m.keys, msg.name, msg.indices)
	return nil
}

// esRestoreFormed picks the snapshot's indices and looks up which
// targets exist (spec §8.1).
func (m *Model) esRestoreFormed(msg overlay.RestoreFormMsg) tea.Cmd {
	r := m.esRestore
	src := esx.SelectIndices(r.indices, msg.Indices)
	if len(src) == 0 {
		m.setStatus("no index of "+r.name+" matches "+msg.Indices, true)
		return nil
	}
	targets, err := esx.RestoreTargets(src, msg.RenamePattern, msg.RenameReplacement)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	spec := es.RestoreSpec{
		Repo: r.repo, Name: r.name, Indices: src, IncludeAliases: msg.IncludeAliases,
		RenamePattern: msg.RenamePattern, RenameReplacement: msg.RenameReplacement,
	}
	b := m.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		out := esRestorePlanMsg{conn: r.conn, repo: r.repo, name: r.name, spec: spec, src: src, targets: targets}
		out.err = withES(ctx, b, r.conn, func(d es.Doer) error {
			all, err := es.Indices(ctx, d)
			if err != nil {
				return err
			}
			status := map[string]bool{} // name → closed
			for _, i := range all {
				status[i.Name] = i.Closed()
			}
			for _, t := range targets {
				if closed, ok := status[t]; ok && closed {
					out.closed = append(out.closed, t)
				} else if ok {
					out.open = append(out.open, t)
				}
			}
			return nil
		})
		return out
	}
}

func (m *Model) esRestorePlanned(msg esRestorePlanMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(esReason(msg.err), true)
		return nil
	}
	if len(msg.open) > 0 {
		m.setStatus(strings.Join(msg.open, ", ")+" exist and are open: close or delete them, or restore under a rename", true)
		return nil
	}
	lines := []string{fmt.Sprintf("restore %s from %s/%s", indexCount(len(msg.src)), msg.repo, msg.name)}
	closed := map[string]bool{}
	for _, c := range msg.closed {
		closed[c] = true
	}
	for i := range min(len(msg.src), 20) {
		l := "  " + msg.src[i] + " → " + msg.targets[i]
		if closed[msg.targets[i]] {
			l += " (closed: restored into it)"
		}
		lines = append(lines, l)
	}
	if n := len(msg.src) - 20; n > 0 {
		lines = append(lines, fmt.Sprintf("  … and %d more", n))
	}
	yesNo := map[bool]string{true: "yes", false: "no"}
	lines = append(lines, "aliases: "+yesNo[msg.spec.IncludeAliases], "global state: no")
	path := "_snapshot/" + msg.repo + "/" + msg.name + "/_restore"
	p := esPlan{Class: esx.Classify("POST", path, nil), Count: -1, Method: "POST", Path: path, Expect: msg.name, Lines: lines}
	return m.confirmES(msg.conn, p, esRestoreRunMsg{conn: msg.conn, spec: msg.spec})
}

func (m *Model) esRestoreRun(msg esRestoreRunMsg) tea.Cmd {
	b := m.backend
	m.setStatus("working…", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		err := withES(ctx, b, msg.conn, func(d es.Doer) error { return es.RestoreSnapshot(ctx, d, msg.spec) })
		return esRestoreDoneMsg{conn: msg.conn, n: len(msg.spec.Indices), err: err}
	}
}

func (m *Model) esRestoreDone(msg esRestoreDoneMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(esReason(msg.err), true)
		return nil
	}
	s := "restore started: " + indexCount(msg.n)
	if h := keyHint(m.keys, keymap.Global, keymap.ServerInfo, "→ Shards to watch"); h != "" {
		s += " · " + h
	}
	m.setStatus(s, false)
	return m.reloadESTree(msg.conn)
}
