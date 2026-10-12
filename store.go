package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	_ "modernc.org/sqlite"
)

type Task struct {
	ID           int64   `json:"id"`
	Title        string  `json:"title"`
	Note         string  `json:"note"`
	Project      string  `json:"project"`
	ProjectLabel string  `json:"project_label"`
	Workspace    string  `json:"workspace"`
	Pane         string  `json:"pane"`
	Session      string  `json:"session"`
	State        string  `json:"state"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	CompletedAt  *string `json:"completed_at"`
	Space        *Space  `json:"space"`
}

type Event struct {
	ID     int64  `json:"id"`
	TaskID int64  `json:"task_id"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	At     string `json:"at"`
}

type Export struct {
	Format     string  `json:"format"`
	Version    int     `json:"version"`
	ExportedAt string  `json:"exported_at"`
	Tasks      []Task  `json:"tasks"`
	Events     []Event `json:"events"`
}

type Store struct {
	db   *sql.DB
	path string
}

const timestampFormat = "2006-01-02T15:04:05.000000+00:00"

func now() string { return time.Now().UTC().Format(timestampFormat) }

func nextTimestamp(previous string) string {
	at := now()
	if at <= previous {
		if t, err := time.Parse(time.RFC3339Nano, previous); err == nil {
			return t.UTC().Add(time.Microsecond).Format(timestampFormat)
		}
	}
	return at
}

func titleText(title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", errors.New("A task needs a title")
	}
	if utf8.RuneCountInString(title) > 500 {
		return "", errors.New("Keep task titles under 501 characters")
	}
	if !utf8.ValidString(title) {
		return "", errors.New("Task titles must contain valid text")
	}
	for _, r := range title {
		if unicode.Is(unicode.C, r) {
			return "", errors.New("Task titles must be a single line without control characters")
		}
	}
	return title, nil
}

func validState(state string) bool { return state == "planned" || state == "doing" || state == "done" }

func dbPath(explicit string) (string, error) {
	path := first(explicit, os.Getenv("HERDR_TODO_DB"))
	if path == "" {
		if dir := os.Getenv("HERDR_PLUGIN_STATE_DIR"); dir != "" {
			path = filepath.Join(dir, "tasks.sqlite3")
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			path = filepath.Join(first(os.Getenv("XDG_STATE_HOME"), filepath.Join(home, ".local/state")), "herdr/plugins/herdr-todo/tasks.sqlite3")
		}
	}
	return resolvePath(path)
}

func openStore(path string) (_ *Store, err error) {
	path, err = resolvePath(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	// Create private from the start, including when the database sits in an
	// existing shared directory. Never truncate an existing database.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: path}
	q := uri.Query()
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Set("_txlock", "immediate")
	uri.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path}
	defer func() {
		if err != nil {
			db.Close()
		}
	}()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version < 0 || version > 2 {
		return nil, fmt.Errorf("Database version %d is newer than this plugin supports", version)
	}
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS tasks (
		 id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL, note TEXT NOT NULL DEFAULT '',
		 project TEXT NOT NULL, project_label TEXT NOT NULL, workspace TEXT NOT NULL DEFAULT '',
		 pane TEXT NOT NULL DEFAULT '', session TEXT NOT NULL DEFAULT '',
		 state TEXT NOT NULL CHECK(state IN ('planned','doing','done')),
		 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, completed_at TEXT)`,
		`CREATE INDEX IF NOT EXISTS tasks_project_state ON tasks(project,state)`,
		`CREATE TABLE IF NOT EXISTS events (id INTEGER PRIMARY KEY AUTOINCREMENT,
		 task_id INTEGER NOT NULL REFERENCES tasks(id), kind TEXT NOT NULL, title TEXT NOT NULL, at TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS events_task ON events(task_id)`,
		`CREATE TABLE IF NOT EXISTS task_spaces (task_id INTEGER PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
		 workspace_id TEXT NOT NULL, label TEXT NOT NULL, session TEXT NOT NULL)`,
		`PRAGMA user_version=2`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

const taskSelect = `SELECT tasks.id,title,note,project,project_label,workspace,pane,tasks.session,state,
created_at,updated_at,completed_at,task_spaces.workspace_id,task_spaces.label,task_spaces.session
FROM tasks LEFT JOIN task_spaces ON task_spaces.task_id=tasks.id`

type querier interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

type scanner interface{ Scan(...any) error }

func scanTask(row scanner) (Task, error) {
	var t Task
	var id, label, session sql.NullString
	err := row.Scan(&t.ID, &t.Title, &t.Note, &t.Project, &t.ProjectLabel, &t.Workspace, &t.Pane, &t.Session,
		&t.State, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt, &id, &label, &session)
	if id.Valid {
		t.Space = &Space{id.String, label.String, session.String}
	}
	return t, err
}

func getTask(q querier, id int64) (Task, error) {
	t, err := scanTask(q.QueryRow(taskSelect+" WHERE tasks.id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		err = fmt.Errorf("Task #%d does not exist", id)
	}
	return t, err
}

func (s *Store) Get(id int64) (Task, error) { return getTask(s.db, id) }

func writeEvent(tx *sql.Tx, id int64, kind, title, at string) error {
	_, err := tx.Exec("INSERT INTO events(task_id,kind,title,at) VALUES(?,?,?,?)", id, kind, title, at)
	return err
}

func writeSpace(tx *sql.Tx, id int64, link *Space) error {
	if link == nil {
		_, err := tx.Exec("DELETE FROM task_spaces WHERE task_id=?", id)
		return err
	}
	_, err := tx.Exec(`INSERT INTO task_spaces(task_id,workspace_id,label,session) VALUES(?,?,?,?)
	ON CONFLICT(task_id) DO UPDATE SET workspace_id=excluded.workspace_id,label=excluded.label,session=excluded.session`,
		id, link.WorkspaceID, link.Label, link.Session)
	return err
}

func (s *Store) Add(title string, c Context, state, note string, space *Space) (Task, error) {
	title, err := titleText(title)
	if err != nil {
		return Task{}, err
	}
	if !validState(state) {
		return Task{}, errors.New("Unknown task state")
	}
	if err := validateSpace(space); err != nil {
		return Task{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	at := now()
	var completed *string
	if state == "done" {
		completed = &at
	}
	result, err := tx.Exec(`INSERT INTO tasks(title,note,project,project_label,workspace,pane,session,state,created_at,updated_at,completed_at)
	 VALUES(?,?,?,?,?,?,?,?,?,?,?)`, title, note, c.Project, c.Label, c.Workspace, c.Pane, c.Session, state, at, at, completed)
	if err != nil {
		return Task{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Task{}, err
	}
	if err = writeSpace(tx, id, space); err != nil {
		return Task{}, err
	}
	for _, kind := range []string{"created", "space_attached", "done"} {
		if kind == "space_attached" && space == nil || kind == "done" && state != "done" {
			continue
		}
		if err = writeEvent(tx, id, kind, title, at); err != nil {
			return Task{}, err
		}
	}
	task, err := getTask(tx, id)
	if err != nil {
		return Task{}, err
	}
	return task, tx.Commit()
}

type Edit struct {
	Title, Note, State *string
	ExpectedUpdatedAt  *string
	SetSpace           bool
	Space              *Space
}

func (s *Store) Edit(id int64, edit Edit) (Task, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	task, err := getTask(tx, id)
	if err != nil {
		return Task{}, err
	}
	if edit.ExpectedUpdatedAt != nil && task.UpdatedAt != *edit.ExpectedUpdatedAt {
		return Task{}, errors.New("This task changed elsewhere. Cancel and reopen it before saving.")
	}
	title, note, state, space := task.Title, task.Note, task.State, task.Space
	if edit.Title != nil {
		title, err = titleText(*edit.Title)
		if err != nil {
			return Task{}, err
		}
	}
	if edit.Note != nil {
		note = *edit.Note
	}
	if edit.State != nil {
		state = *edit.State
	}
	if !validState(state) {
		return Task{}, errors.New("Unknown task state")
	}
	if edit.SetSpace {
		space = edit.Space
		if err := validateSpace(space); err != nil {
			return Task{}, err
		}
	}
	if title == task.Title && note == task.Note && state == task.State && sameSpace(space, task.Space) {
		return task, tx.Commit()
	}
	at, completed := nextTimestamp(task.UpdatedAt), task.CompletedAt
	if state != task.State {
		completed = nil
		if state == "done" {
			completed = &at
		}
	}
	if _, err = tx.Exec("UPDATE tasks SET title=?,note=?,state=?,completed_at=?,updated_at=? WHERE id=?", title, note, state, completed, at, id); err != nil {
		return Task{}, err
	}
	if !sameSpace(space, task.Space) {
		if err = writeSpace(tx, id, space); err != nil {
			return Task{}, err
		}
		kind := "space_attached"
		if space == nil {
			kind = "space_detached"
		}
		if err = writeEvent(tx, id, kind, title, at); err != nil {
			return Task{}, err
		}
	}
	if title != task.Title || note != task.Note {
		if err = writeEvent(tx, id, "edited", title, at); err != nil {
			return Task{}, err
		}
	}
	if state != task.State {
		if err = writeEvent(tx, id, state, title, at); err != nil {
			return Task{}, err
		}
	}
	task, err = getTask(tx, id)
	if err != nil {
		return Task{}, err
	}
	return task, tx.Commit()
}

func (s *Store) SetState(id int64, state string) (Task, error) {
	return s.Edit(id, Edit{State: &state})
}

func (s *Store) UndoState(before, after Task) (Task, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	current, err := getTask(tx, after.ID)
	if err != nil {
		return Task{}, err
	}
	if before.ID != after.ID || current.UpdatedAt != after.UpdatedAt {
		return Task{}, errors.New("This task changed since then. Open it to update its status.")
	}
	at := nextTimestamp(current.UpdatedAt)
	if _, err = tx.Exec("UPDATE tasks SET state=?,completed_at=?,updated_at=? WHERE id=?", before.State, before.CompletedAt, at, current.ID); err != nil {
		return Task{}, err
	}
	if err = writeEvent(tx, current.ID, "undo", current.Title, at); err != nil {
		return Task{}, err
	}
	current, err = getTask(tx, current.ID)
	if err != nil {
		return Task{}, err
	}
	return current, tx.Commit()
}

type ListOptions struct {
	Project                          *string
	View, Search, Session, Workspace string
}

func containsFold(text, query string) bool {
	return strings.Contains(cases.Fold().String(text), cases.Fold().String(query))
}

func listTasks(q querier, opt ListOptions) ([]Task, error) {
	clauses, args := []string{}, []any{}
	if opt.Project != nil {
		if opt.Session != "" && opt.Workspace != "" {
			clauses = append(clauses, "(project=? OR (task_spaces.session=? AND task_spaces.workspace_id=?))")
			args = append(args, *opt.Project, opt.Session, opt.Workspace)
		} else {
			clauses = append(clauses, "project=?")
			args = append(args, *opt.Project)
		}
	}
	switch opt.View {
	case "", "active":
		clauses = append(clauses, "state!='done'")
	case "today", "history":
		clauses = append(clauses, "state='done'")
	case "all":
	default:
		return nil, errors.New("Unknown task view")
	}
	query := taskSelect
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	if opt.View == "today" || opt.View == "history" {
		query += " ORDER BY completed_at DESC,tasks.id DESC"
	} else {
		query += " ORDER BY CASE state WHEN 'doing' THEN 0 WHEN 'planned' THEN 1 ELSE 2 END,tasks.id DESC"
	}
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []Task{}
	today := time.Now().In(time.Local).Format("2006-01-02")
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		if opt.View == "today" {
			if t.CompletedAt == nil {
				continue
			}
			stamp, err := time.Parse(time.RFC3339Nano, *t.CompletedAt)
			if err != nil {
				return nil, fmt.Errorf("Task #%d has an invalid completion date: %w", t.ID, err)
			}
			if stamp.In(time.Local).Format("2006-01-02") != today {
				continue
			}
		}
		label := ""
		if t.Space != nil {
			label = t.Space.Label
		}
		if opt.Search != "" && !containsFold(t.Title, opt.Search) && !containsFold(t.Note, opt.Search) && !containsFold(label, opt.Search) {
			continue
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (s *Store) List(opt ListOptions) ([]Task, error) { return listTasks(s.db, opt) }

func listEvents(q querier, id *int64) ([]Event, error) {
	query, args := "SELECT id,task_id,kind,title,at FROM events", []any{}
	if id != nil {
		if _, err := getTask(q, *id); err != nil {
			return nil, err
		}
		query += " WHERE task_id=?"
		args = append(args, *id)
	}
	rows, err := q.Query(query+" ORDER BY id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.TaskID, &e.Kind, &e.Title, &e.At); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (s *Store) Events(id *int64) ([]Event, error) { return listEvents(s.db, id) }

func (s *Store) Export() (Export, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Export{}, err
	}
	defer tx.Rollback()
	tasks, err := listTasks(tx, ListOptions{View: "all"})
	if err != nil {
		return Export{}, err
	}
	events, err := listEvents(tx, nil)
	if err != nil {
		return Export{}, err
	}
	return Export{"herdr-todo", 2, now(), tasks, events}, tx.Commit()
}
