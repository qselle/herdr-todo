package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var testContext = Context{Project: "/work/project", Label: "Project", Workspace: "w1", Pane: "w1:p1", Session: "/tmp/herdr-origin.sock"}

func ptr[T any](value T) *T { return &value }

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "tasks.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func addTask(t *testing.T, s *Store, title, state string) Task {
	t.Helper()
	task, err := s.Add(title, testContext, state, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func copyFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/v0.1.0.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "old.sqlite3")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPythonDatabaseCompatibility(t *testing.T) {
	s, err := openStore(copyFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw, err := os.ReadFile("testdata/v0.1.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var want Export
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	got.ExportedAt = want.ExportedAt
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Python database changed on opening\ngot: %#v\nwant: %#v", got, want)
	}
	old, err := s.Get(2)
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.Edit(old.ID, Edit{Note: ptr("edited from Go")})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(old.CompletedAt, after.CompletedAt) {
		t.Fatal("note edit moved the completion date")
	}
	newTask := addTask(t, s, "First Go task", "planned")
	if newTask.ID != 4 {
		t.Fatalf("task IDs did not continue: %d", newTask.ID)
	}
	info, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("database permissions = %v", info.Mode())
	}
}

func TestLegacySchemaAndFutureVersion(t *testing.T) {
	s := testStore(t)
	task := addTask(t, s, "Keep me", "done")
	if _, err := s.db.Exec("DROP TABLE task_spaces; PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	reopened, err := openStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get(task.ID)
	if err != nil || !reflect.DeepEqual(got, task) {
		t.Fatalf("v1 migration: %+v %v", got, err)
	}
	if _, err := reopened.db.Exec("PRAGMA user_version=3"); err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	if newer, err := openStore(s.path); err == nil {
		newer.Close()
		t.Fatal("accepted a future schema")
	}
	var version int
	// A failed open must never lower user_version.
	if err := s.db.Ping(); err == nil {
		t.Fatal("test connection should be closed")
	}
	raw, err := sql.Open("sqlite", s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("future version was changed to %d", version)
	}
}

func TestAtomicEditAndUndoConflict(t *testing.T) {
	s := testStore(t)
	before := addTask(t, s, "Draft", "planned")
	space := &Space{"w2", "Lab", testContext.Session}
	after, err := s.Edit(before.ID, Edit{Title: ptr("Revised"), Note: ptr("More detail"), State: ptr("done"), SetSpace: true, Space: space, ExpectedUpdatedAt: &before.UpdatedAt})
	if err != nil {
		t.Fatal(err)
	}
	if after.State != "done" || after.CompletedAt == nil || !sameSpace(after.Space, space) {
		t.Fatalf("edit: %+v", after)
	}
	events, _ := s.Events(&before.ID)
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	if !reflect.DeepEqual(kinds, []string{"created", "space_attached", "edited", "done"}) {
		t.Fatal(kinds)
	}
	if _, err := s.Edit(before.ID, Edit{Title: ptr("Stale"), SetSpace: true, ExpectedUpdatedAt: &before.UpdatedAt}); err == nil {
		t.Fatal("stale edit accepted")
	}
	current, _ := s.Get(before.ID)
	if !reflect.DeepEqual(current, after) {
		t.Fatal("failed edit partially committed")
	}
	restored, err := s.UndoState(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if restored.State != "planned" || restored.CompletedAt != nil || restored.Title != "Revised" || !sameSpace(restored.Space, space) {
		t.Fatalf("undo changed unrelated fields: %+v", restored)
	}
	done, _ := s.SetState(before.ID, "done")
	_, _ = s.Edit(before.ID, Edit{Note: ptr("Other client")})
	if _, err := s.UndoState(restored, done); err == nil {
		t.Fatal("undo overwrote another client")
	}
}

func TestTransactionRollbackOnEventFailure(t *testing.T) {
	s := testStore(t)
	task := addTask(t, s, "Before", "planned")
	if _, err := s.db.Exec(`CREATE TRIGGER reject_events BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT, 'journal unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := s.Edit(task.ID, Edit{Title: ptr("After"), State: ptr("done"), SetSpace: true, Space: &Space{"w2", "Lab", testContext.Session}})
	if err == nil {
		t.Fatal("expected journal failure")
	}
	got, _ := s.Get(task.ID)
	if !reflect.DeepEqual(got, task) {
		t.Fatalf("partial edit committed: %+v", got)
	}
	if _, err := s.Add("No partial capture", testContext, "done", "", nil); err == nil {
		t.Fatal("expected capture failure")
	}
	rows, _ := s.List(ListOptions{View: "all"})
	if len(rows) != 1 {
		t.Fatal("failed capture left a task")
	}
}

func TestNoOpAndCompletionRestoration(t *testing.T) {
	s := testStore(t)
	before := addTask(t, s, "Finished", "done")
	after, err := s.SetState(before.ID, "done")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("idempotent completion: %+v %v", after, err)
	}
	after, err = s.Edit(before.ID, Edit{Title: &before.Title, Note: &before.Note, SetSpace: true})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("no-op edit changed task")
	}
	reopened, _ := s.SetState(before.ID, "planned")
	restored, err := s.UndoState(before, reopened)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.CompletedAt, before.CompletedAt) {
		t.Fatal("undo did not restore original completion date")
	}
	events, _ := s.Events(&before.ID)
	if len(events) != 4 {
		t.Fatalf("duplicate events: %+v", events)
	}
}

func TestProjectAndSpaceScope(t *testing.T) {
	s := testStore(t)
	link := &Space{"w2", "Straße Lab", "session-A"}
	task, err := s.Add("Research", testContext, "planned", "Unicode 日本語", link)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		project, session, workspace, search string
		count                               int
	}{
		{testContext.Project, "", "", "", 1}, {"/other", "session-A", "w2", "", 1},
		{"/other", "session-B", "w2", "", 0}, {"/other", "session-A", "w1", "", 0},
		{testContext.Project, "", "", "STRASSE", 1}, {testContext.Project, "", "", "日本", 1},
	} {
		rows, err := s.List(ListOptions{Project: &c.project, Session: c.session, Workspace: c.workspace, Search: c.search})
		if err != nil || len(rows) != c.count {
			t.Fatalf("scope %+v: %+v, %v", c, rows, err)
		}
	}
	detached, err := s.Edit(task.ID, Edit{SetSpace: true})
	if err != nil || detached.Space != nil || detached.Project != task.Project {
		t.Fatalf("detach changed origin: %+v %v", detached, err)
	}
}

func TestOrderingAndLocalToday(t *testing.T) {
	s := testStore(t)
	p := addTask(t, s, "Next", "planned")
	d := addTask(t, s, "Working", "doing")
	addTask(t, s, "Newest", "planned")
	finished := addTask(t, s, "Done", "done")
	rows, _ := s.List(ListOptions{View: "active"})
	if len(rows) != 3 || rows[0].ID != d.ID || rows[2].ID != p.ID {
		t.Fatalf("ordering: %+v", rows)
	}
	prior := time.Now().In(time.Local).AddDate(0, 0, -1).UTC().Format(timestampFormat)
	if _, err := s.db.Exec("UPDATE tasks SET completed_at=? WHERE id=?", prior, finished.ID); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.List(ListOptions{View: "today"})
	if len(rows) != 0 {
		t.Fatal("yesterday included in today")
	}
	if _, err := s.db.Exec("UPDATE tasks SET completed_at=? WHERE id=?", now(), finished.ID); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.List(ListOptions{View: "today"})
	if len(rows) != 1 {
		t.Fatal("today completion missing")
	}
}

func TestInvalidInputDoesNotWrite(t *testing.T) {
	s := testStore(t)
	for _, title := range []string{"", "  ", "bad\x00title", "bad\nline", "a\u200bb", strings.Repeat("界", 501)} {
		if _, err := s.Add(title, testContext, "planned", "", nil); err == nil {
			t.Fatalf("accepted %q", title)
		}
	}
	if _, err := s.Add("valid", testContext, "unknown", "", nil); err == nil {
		t.Fatal("accepted invalid state")
	}
	if _, err := s.Add("valid", testContext, "planned", "", &Space{}); err == nil {
		t.Fatal("accepted invalid space")
	}
	if _, err := s.Get(999); err == nil {
		t.Fatal("missing task should fail")
	}
	export, err := s.Export()
	if err != nil || len(export.Tasks) != 0 || len(export.Events) != 0 {
		t.Fatalf("invalid capture wrote data: %+v %v", export, err)
	}
}

func TestConcurrentOpenAndCapture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.sqlite3")
	const workers = 8
	var wg sync.WaitGroup
	errors := make(chan error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s, err := openStore(path)
			if err != nil {
				errors <- err
				return
			}
			defer s.Close()
			_, err = s.Add("Concurrent task", testContext, "planned", "", nil)
			errors <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	data, err := s.Export()
	if err != nil || len(data.Tasks) != workers || len(data.Events) != workers {
		t.Fatalf("lost writes: %+v %v", data, err)
	}
}
