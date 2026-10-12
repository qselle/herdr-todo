package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func clearHerdrEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"HERDR_TODO_CONTEXT", "HERDR_PLUGIN_CONTEXT_JSON", "HERDR_PLUGIN_ROOT", "HERDR_ACTIVE_PANE_CWD", "HERDR_WORKSPACE_ID", "HERDR_PANE_ID", "HERDR_SOCKET_PATH", "HERDR_TODO_DB", "HERDR_PLUGIN_STATE_DIR", "HERDR_ENV"} {
		t.Setenv(name, "")
	}
}

func TestContextResolutionAndPopupEnvironment(t *testing.T) {
	clearHerdrEnvironment(t)
	dir := canonicalTempDir(t)
	project := filepath.Join(dir, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "alias")
	if err := os.Symlink(project, link); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]string{"workspace_cwd": link, "workspace_label": "Workspace", "workspace_id": "w8", "focused_pane_id": "w8:p2"})
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", string(data))
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/context.sock")
	c, err := currentContext("")
	if err != nil || c.Project != project || c.Label != "Workspace" || c.Workspace != "w8" || c.Pane != "w8:p2" {
		t.Fatalf("context %+v %v", c, err)
	}
	carried, _ := json.Marshal(c)
	t.Setenv("HERDR_TODO_CONTEXT", string(carried))
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", `{"workspace_cwd":"/wrong"}`)
	again, err := currentContext("")
	if err != nil || again != c {
		t.Fatalf("carried context %+v %v", again, err)
	}
	overridden, err := currentContext(dir)
	if err != nil || overridden.Project != dir || overridden.Label != filepath.Base(dir) {
		t.Fatalf("override %+v %v", overridden, err)
	}
	// The fake command prints one argument per line; it never contacts Herdr.
	fake := filepath.Join(dir, "fake-herdr")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", fake)
	out, err := openPopup(c, filepath.Join(dir, "custom.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if strings.Contains(text, "--workspace") || strings.Contains(text, "--target-pane") || !strings.Contains(text, "HERDR_TODO_CONTEXT="+string(carried)) || !strings.Contains(text, "HERDR_TODO_DB="+filepath.Join(dir, "custom.sqlite3")) {
		t.Fatal(text)
	}
}

func TestInvalidOrMissingPluginContext(t *testing.T) {
	clearHerdrEnvironment(t)
	t.Setenv("HERDR_PLUGIN_ROOT", "/plugin/source")
	if _, err := currentContext(""); err == nil {
		t.Fatal("missing project fell back to plugin source")
	}
	for _, raw := range []string{"[]", "null", "not JSON", `{"workspace_cwd":12}`} {
		t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", raw)
		if _, err := currentContext(""); err == nil {
			t.Fatalf("accepted invalid context %s", raw)
		}
	}
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", "{}")
	t.Setenv("HERDR_ACTIVE_PANE_CWD", canonicalTempDir(t))
	if _, err := currentContext(""); err != nil {
		t.Fatal(err)
	}
}

func TestDatabasePathPrecedence(t *testing.T) {
	clearHerdrEnvironment(t)
	dir := canonicalTempDir(t)
	t.Setenv("XDG_STATE_HOME", dir)
	path, err := dbPath("")
	if err != nil || path != filepath.Join(dir, "herdr/plugins/herdr-todo/tasks.sqlite3") {
		t.Fatalf("fallback %s %v", path, err)
	}
	t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(dir, "plugin-state"))
	path, _ = dbPath("")
	if path != filepath.Join(dir, "plugin-state/tasks.sqlite3") {
		t.Fatal(path)
	}
	t.Setenv("HERDR_TODO_DB", filepath.Join(dir, "env.sqlite3"))
	path, _ = dbPath("")
	if path != filepath.Join(dir, "env.sqlite3") {
		t.Fatal(path)
	}
	path, _ = dbPath(filepath.Join(dir, "explicit.sqlite3"))
	if path != filepath.Join(dir, "explicit.sqlite3") {
		t.Fatal(path)
	}
}

func TestCLIWorkflowAndInterspersedFlags(t *testing.T) {
	clearHerdrEnvironment(t)
	db := filepath.Join(canonicalTempDir(t), "tasks.sqlite3")
	run := func(args ...string) string {
		t.Helper()
		var out, errout bytes.Buffer
		args = append([]string{"--db", db, "--project", "/work/cli"}, args...)
		if err := runCLI(args, &out, &errout); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	var task Task
	if err := json.Unmarshal([]byte(run("add", "Finished 日本語", "--done", "--note", "Original")), &task); err != nil {
		t.Fatal(err)
	}
	if task.State != "done" || task.Note != "Original" || task.Project != "/work/cli" {
		t.Fatalf("capture %+v", task)
	}
	run("edit", "1", "--note", "")
	run("reopen", "1")
	run("doing", "1")
	run("done", "1")
	var tasks []Task
	if err := json.Unmarshal([]byte(run("list", "--all-projects", "--view", "today", "--search", "日本")), &tasks); err != nil || len(tasks) != 1 {
		t.Fatalf("list %+v %v", tasks, err)
	}
	var events []Event
	if err := json.Unmarshal([]byte(run("journal", "--id", "1")), &events); err != nil || len(events) != 6 {
		t.Fatalf("journal %+v %v", events, err)
	}
	var exported Export
	if err := json.Unmarshal([]byte(run("export")), &exported); err != nil || exported.Format != "herdr-todo" || exported.Version != 2 {
		t.Fatalf("export %+v %v", exported, err)
	}
	if strings.TrimSpace(run("path")) != db {
		t.Fatal("CLI path override ignored")
	}
	if strings.TrimSpace(run("--version")) != version() {
		t.Fatal("version mismatch")
	}
	if !strings.Contains(run("--help"), "Usage:") {
		t.Fatal("missing help")
	}
	for _, args := range [][]string{{"edit", "1"}, {"done", "oops"}, {"list", "--unknown"}, {"add", "title", "--note"}, {"add", ""}, {"journal", "--id", "-1"}, {"path", "extra"}} {
		var output bytes.Buffer
		if err := runCLI(append([]string{"--db", db}, args...), &output, &output); err == nil {
			t.Fatalf("invalid command accepted: %v", args)
		}
	}
}

func TestSpacesGuardSessionAndClosedWorkspace(t *testing.T) {
	clearHerdrEnvironment(t)
	s := &HerdrSpaces{context: testContext}
	if _, err := s.Refresh(); err == nil {
		t.Fatal("controlled Herdr outside a managed pane")
	}
	t.Setenv("HERDR_ENV", "1")
	dir := canonicalTempDir(t)
	fake := filepath.Join(dir, "herdr")
	// A same-named live workspace must never replace a missing saved ID.
	script := `#!/bin/sh
if [ "$1 $2" = 'workspace list' ]; then
    printf '%s\n' '{"result":{"workspaces":[{"workspace_id":"w2","label":"Same name"}]}}'
elif [ "$1 $2" = 'workspace focus' ]; then
    printf '%s\n' "$HERDR_SOCKET_PATH $3" > "$HERDR_TEST_FOCUS"
    printf '%s\n' '{"result":{}}'
else
    exit 1
fi
`
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "focus")
	t.Setenv("HERDR_BIN_PATH", fake)
	t.Setenv("HERDR_TEST_FOCUS", marker)
	for _, space := range []*Space{nil, {"w2", "Same name", "different-session"}, {"closed", "Same name", testContext.Session}} {
		if err := s.Focus(space); err == nil {
			t.Fatalf("focused invalid link %+v", space)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("invalid link ran focus")
	}
	if err := s.Focus(&Space{"w2", "Old name", testContext.Session}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(data)) != testContext.Session+" w2" {
		t.Fatalf("wrong session/ID: %q %v", data, err)
	}
	if s.Label(&Space{"w2", "Old name", testContext.Session}) != "Same name" {
		t.Fatal("cached label not refreshed")
	}
}
