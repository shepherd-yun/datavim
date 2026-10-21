package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/shepherd-yun/datavim/internal/config"
)

// workspaceOptions are the command line's say on the workspace.
type workspaceOptions struct {
	name   string // -w / --workspace
	env    string // DATAVIM_WORKSPACE
	create bool   // --create
}

// selectWorkspace picks the workspace: -w, else DATAVIM_WORKSPACE, else the
// default. A missing workspace is an error unless --create asks for it, so a
// typo never opens an empty workspace.
func selectWorkspace(cfgRoot, stateRoot string, o workspaceOptions) (config.Workspace, error) {
	name := o.name
	if name == "" {
		name = o.env
	}
	if name == "" {
		if o.create {
			return config.Workspace{}, errors.New("--create needs a workspace name (-w or DATAVIM_WORKSPACE)")
		}
		name = config.DefaultWorkspace
	}
	ws, err := config.ResolveWorkspace(cfgRoot, stateRoot, name)
	if err != nil {
		return ws, err
	}
	if ws.IsDefault() {
		if o.create {
			return ws, errors.New("--create cannot be used with the default workspace")
		}
		return ws, nil
	}
	ok, err := ws.Exists()
	switch {
	case err != nil:
		return ws, err
	case ok:
		return ws, nil
	case o.create:
		return ws, ws.Create()
	}
	have, err := config.ListWorkspaces(cfgRoot)
	if err != nil {
		return ws, err
	}
	return ws, fmt.Errorf("workspace %q does not exist (have: %s)\ncreate it with: datavim -w %s --create",
		name, strings.Join(have, ", "), name)
}

// keychainPrefix keeps the passwords of same-named connections in different
// workspaces apart; the default workspace keeps its accounts unprefixed.
func keychainPrefix(ws config.Workspace) string {
	if ws.IsDefault() {
		return ""
	}
	return ws.Name + "/"
}
