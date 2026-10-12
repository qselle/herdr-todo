package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Context struct {
	Project   string `json:"project"`
	Label     string `json:"label"`
	Workspace string `json:"workspace"`
	Pane      string `json:"pane"`
	Session   string `json:"session"`
}

func resolvePath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// Resolve existing ancestors too, matching pathlib.resolve for new paths.
	parent := abs
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		tail = append(tail, filepath.Base(parent))
		next := filepath.Dir(parent)
		if next == parent {
			return "", err
		}
		parent = next
	}
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func currentContext(project string) (Context, error) {
	carried := os.Getenv("HERDR_TODO_CONTEXT")
	raw := first(carried, os.Getenv("HERDR_PLUGIN_CONTEXT_JSON"), "{}")
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return Context{}, errors.New("Herdr workspace context must be a JSON object")
	}
	if carried != "" {
		var c Context
		if err := json.Unmarshal([]byte(raw), &c); err != nil || c.Project == "" || c.Label == "" {
			return Context{}, errors.New("Herdr supplied invalid workspace context")
		}
		if project == "" {
			return c, nil
		}
		path, err := resolvePath(project)
		return Context{Project: path, Label: filepath.Base(path)}, err
	}
	var data struct {
		WorkspaceCWD string `json:"workspace_cwd"`
		PaneCWD      string `json:"focused_pane_cwd"`
		Label        string `json:"workspace_label"`
		Workspace    string `json:"workspace_id"`
		Pane         string `json:"focused_pane_id"`
	}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return Context{}, errors.New("Herdr supplied invalid workspace context")
	}
	cwd := first(project, data.WorkspaceCWD, data.PaneCWD, os.Getenv("HERDR_ACTIVE_PANE_CWD"))
	if cwd == "" && os.Getenv("HERDR_PLUGIN_ROOT") != "" {
		return Context{}, errors.New("No project directory in Herdr context; use --project PATH")
	}
	path, err := resolvePath(first(cwd, "."))
	if err != nil {
		return Context{}, err
	}
	label := data.Label
	if project != "" || label == "" {
		label = filepath.Base(path)
	}
	return Context{path, label, first(data.Workspace, os.Getenv("HERDR_WORKSPACE_ID")), first(data.Pane, os.Getenv("HERDR_PANE_ID")), os.Getenv("HERDR_SOCKET_PATH")}, nil
}

func herdrCommand(timeout time.Duration, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, first(os.Getenv("HERDR_BIN_PATH"), "herdr"), args...)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("Herdr did not respond: %w", ctx.Err())
	}
	if err != nil {
		return nil, fmt.Errorf("%s", first(strings.TrimSpace(stderr.String()), strings.TrimSpace(string(out)), err.Error()))
	}
	return out, nil
}

func openPopup(c Context, db string) ([]byte, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	args := []string{"plugin", "pane", "open", "--plugin", "herdr-todo", "--entrypoint", "tasks", "--env", "HERDR_TODO_CONTEXT=" + string(data)}
	// Popup placement rejects explicit workspace/pane targets. Carry the origin
	// so a focus change cannot reassign newly captured tasks to another project.
	if db = first(db, os.Getenv("HERDR_TODO_DB")); db != "" {
		db, err = resolvePath(db)
		if err != nil {
			return nil, err
		}
		args = append(args, "--env", "HERDR_TODO_DB="+db)
	}
	return herdrCommand(15*time.Second, os.Environ(), args...)
}
