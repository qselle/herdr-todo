"""Transactional task storage. All timestamps are ISO 8601 in UTC."""
from datetime import datetime, timezone
import os
from pathlib import Path
import sqlite3
import unicodedata

from .spaces import space_link

STATES = ("planned", "doing", "done")
UNCHANGED = object()
TASK_SELECT = """SELECT tasks.*, task_spaces.workspace_id AS space_id,
    task_spaces.label AS space_label, task_spaces.session AS space_session
    FROM tasks LEFT JOIN task_spaces ON task_spaces.task_id=tasks.id"""


def task_record(row):
    task = dict(row)
    workspace_id, label, session = (task.pop(key) for key in ("space_id", "space_label", "space_session"))
    task["space"] = {"workspace_id": workspace_id, "label": label, "session": session} if workspace_id else None
    return task


def now():
    return datetime.now(timezone.utc).isoformat(timespec="microseconds")


def title_text(value):
    value = value.strip()
    if not value:
        raise ValueError("A task needs a title")
    if len(value) > 500:
        raise ValueError("Keep task titles under 501 characters")
    if any(unicodedata.category(c).startswith("C") for c in value):
        raise ValueError("Task titles must be a single line without control characters")
    return value


def db_path(explicit=None):
    if explicit or os.environ.get("HERDR_TODO_DB"):
        return Path(explicit or os.environ["HERDR_TODO_DB"]).expanduser()
    if os.environ.get("HERDR_PLUGIN_STATE_DIR"):
        return Path(os.environ["HERDR_PLUGIN_STATE_DIR"]) / "tasks.sqlite3"
    # Match Herdr's per-user plugin state directory for standalone CLI use.
    base = Path(os.environ.get("XDG_STATE_HOME", Path.home() / ".local/state"))
    return base / "herdr/plugins/herdr-todo/tasks.sqlite3"


class Store:
    def __init__(self, path):
        self.path = Path(path).expanduser().resolve()
        self.path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        self.db = sqlite3.connect(self.path, timeout=5)
        self.db.row_factory = sqlite3.Row
        try:
            self.db.execute("PRAGMA foreign_keys=ON")
            # Lock before checking the version so concurrent openers cannot race
            # a migration or downgrade a database from a newer release.
            with self.db:
                self.db.execute("BEGIN IMMEDIATE")
                version = self.db.execute("PRAGMA user_version").fetchone()[0]
                if version not in (0, 1, 2):
                    raise ValueError(f"Database version {version} is newer than this plugin supports")
                self.db.execute("""
                    CREATE TABLE IF NOT EXISTS tasks (
                        id INTEGER PRIMARY KEY AUTOINCREMENT,
                        title TEXT NOT NULL,
                        note TEXT NOT NULL DEFAULT '',
                        project TEXT NOT NULL,
                        project_label TEXT NOT NULL,
                        workspace TEXT NOT NULL DEFAULT '',
                        pane TEXT NOT NULL DEFAULT '',
                        session TEXT NOT NULL DEFAULT '',
                        state TEXT NOT NULL CHECK(state IN ('planned','doing','done')),
                        created_at TEXT NOT NULL,
                        updated_at TEXT NOT NULL,
                        completed_at TEXT
                    )
                """)
                self.db.execute("CREATE INDEX IF NOT EXISTS tasks_project_state ON tasks(project, state)")
                self.db.execute("""
                    CREATE TABLE IF NOT EXISTS events (
                        id INTEGER PRIMARY KEY AUTOINCREMENT,
                        task_id INTEGER NOT NULL REFERENCES tasks(id),
                        kind TEXT NOT NULL,
                        title TEXT NOT NULL,
                        at TEXT NOT NULL
                    )
                """)
                self.db.execute("CREATE INDEX IF NOT EXISTS events_task ON events(task_id)")
                self.db.execute("""
                    CREATE TABLE IF NOT EXISTS task_spaces (
                        task_id INTEGER PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
                        workspace_id TEXT NOT NULL,
                        label TEXT NOT NULL,
                        session TEXT NOT NULL
                    )
                """)
                self.db.execute("PRAGMA user_version=2")
            os.chmod(self.path, 0o600)
        except Exception:
            self.db.close()
            raise

    def close(self):
        self.db.close()

    def get(self, task_id):
        row = self.db.execute(TASK_SELECT + " WHERE tasks.id=?", (task_id,)).fetchone()
        if row is None:
            raise ValueError(f"Task #{task_id} does not exist")
        return task_record(row)

    def _event(self, task_id, kind, title, at):
        self.db.execute("INSERT INTO events(task_id,kind,title,at) VALUES(?,?,?,?)",
                        (task_id, kind, title, at))

    def _space(self, task_id, space):
        if space is None:
            self.db.execute("DELETE FROM task_spaces WHERE task_id=?", (task_id,))
        else:
            self.db.execute("""INSERT INTO task_spaces(task_id,workspace_id,label,session) VALUES(?,?,?,?)
                ON CONFLICT(task_id) DO UPDATE SET workspace_id=excluded.workspace_id,
                    label=excluded.label,session=excluded.session""",
                            (task_id, space["workspace_id"], space["label"], space["session"]))

    def add(self, title, context, state="planned", note="", space=None):
        title = title_text(title)
        space = space_link(space)
        if state not in STATES:
            raise ValueError("Unknown task state")
        at = now()
        with self.db:
            cursor = self.db.execute("""
                INSERT INTO tasks(title,note,project,project_label,workspace,pane,session,
                                  state,created_at,updated_at,completed_at)
                VALUES(?,?,?,?,?,?,?,?,?,?,?)
            """, (title, note, context.project, context.label, context.workspace, context.pane,
                  context.session, state, at, at, at if state == "done" else None))
            task_id = cursor.lastrowid
            self._space(task_id, space)
            self._event(task_id, "created", title, at)
            if space:
                self._event(task_id, "space_attached", title, at)
            if state == "done":
                self._event(task_id, "done", title, at)
        return self.get(task_id)

    def set_state(self, task_id, state):
        if state not in STATES:
            raise ValueError("Unknown task state")
        with self.db:
            self.db.execute("BEGIN IMMEDIATE")
            task = self.get(task_id)
            if task["state"] != state:
                at = now()
                self.db.execute("UPDATE tasks SET state=?,updated_at=?,completed_at=? WHERE id=?",
                                (state, at, at if state == "done" else None, task_id))
                self._event(task_id, state, task["title"], at)
        return self.get(task_id)

    def edit(self, task_id, title=None, note=None, state=None, expected_updated_at=None, space=UNCHANGED):
        with self.db:
            self.db.execute("BEGIN IMMEDIATE")
            task = self.get(task_id)
            if expected_updated_at is not None and task["updated_at"] != expected_updated_at:
                raise ValueError("This task changed elsewhere. Cancel and reopen it before saving.")
            title = task["title"] if title is None else title_text(title)
            note = task["note"] if note is None else note
            state = task["state"] if state is None else state
            space = task["space"] if space is UNCHANGED else space_link(space)
            if state not in STATES:
                raise ValueError("Unknown task state")
            at = now()
            completed = task["completed_at"] if state == task["state"] else at if state == "done" else None
            if (title, note, state, space) != (task["title"], task["note"], task["state"], task["space"]):
                self.db.execute("UPDATE tasks SET title=?,note=?,state=?,completed_at=?,updated_at=? WHERE id=?",
                                (title, note, state, completed, at, task_id))
                if space != task["space"]:
                    self._space(task_id, space)
                    self._event(task_id, "space_attached" if space else "space_detached", title, at)
                if (title, note) != (task["title"], task["note"]):
                    self._event(task_id, "edited", title, at)
                if state != task["state"]:
                    self._event(task_id, state, title, at)
        return self.get(task_id)

    def undo_state(self, before, after):
        """Restore a UI state change without overwriting another client's work."""
        with self.db:
            self.db.execute("BEGIN IMMEDIATE")
            current = self.get(after["id"])
            if before["id"] != after["id"] or current["updated_at"] != after["updated_at"]:
                raise ValueError("This task changed since then. Open it to update its status.")
            at = now()
            self.db.execute("UPDATE tasks SET state=?,completed_at=?,updated_at=? WHERE id=?",
                            (before["state"], before["completed_at"], at, current["id"]))
            self._event(current["id"], "undo", current["title"], at)
        return self.get(after["id"])

    def list(self, project=None, view="active", search="", linked_here=None):
        clauses, args = [], []
        if project is not None:
            clauses.append("(project=? OR (task_spaces.session=? AND task_spaces.workspace_id=?))" if linked_here else "project=?")
            args.append(project)
            if linked_here:
                args.extend(linked_here)
        if view == "active":
            clauses.append("state != 'done'")
        elif view in ("history", "today"):
            clauses.append("state = 'done'")
        elif view != "all":
            raise ValueError("Unknown task view")
        if search:
            clauses.append("(instr(lower(title), lower(?)) > 0 OR instr(lower(note), lower(?)) > 0 OR instr(lower(task_spaces.label), lower(?)) > 0)")
            args.extend((search, search, search))
        where = " WHERE " + " AND ".join(clauses) if clauses else ""
        order = ("completed_at DESC, id DESC" if view in ("history", "today") else
                 "CASE state WHEN 'doing' THEN 0 WHEN 'planned' THEN 1 ELSE 2 END, id DESC")
        rows = [task_record(r) for r in self.db.execute(TASK_SELECT + where + " ORDER BY " + order, args)]
        if view == "today":
            today = datetime.now().astimezone().date()
            rows = [r for r in rows if datetime.fromisoformat(r["completed_at"]).astimezone().date() == today]
        return rows

    def events(self, task_id=None):
        if task_id is not None:
            self.get(task_id)
            return [dict(r) for r in self.db.execute("SELECT * FROM events WHERE task_id=? ORDER BY id", (task_id,))]
        return [dict(r) for r in self.db.execute("SELECT * FROM events ORDER BY id")]

    def export(self):
        # One read transaction makes the task table and journal a consistent snapshot.
        with self.db:
            self.db.execute("BEGIN")
            return {"format": "herdr-todo", "version": 2, "exported_at": now(),
                    "tasks": self.list(view="all"), "events": self.events()}
