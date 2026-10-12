"""Storage and context checks; all data stays in temporary directories."""
from concurrent.futures import ThreadPoolExecutor
from contextlib import closing
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from herdr_todo.context import Context, open_popup
from herdr_todo.store import Store, db_path
from herdr_todo.spaces import Spaces


class Tasks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / "tasks.sqlite3"
        self.store = Store(self.path)
        self.addCleanup(self.store.close)
        self.context = Context("/projects/infra", "infra", "w1", "w1:p1", "/tmp/session.sock")

    def test_persistence_and_completion_journal(self):
        task = self.store.add("Check storage", self.context)
        self.store.set_state(task["id"], "doing")
        done = self.store.set_state(task["id"], "done")
        self.store.set_state(task["id"], "done")  # Idempotent completion.
        with closing(Store(self.path)) as reopened:
            self.assertEqual(reopened.get(task["id"])["completed_at"], done["completed_at"])
            self.assertEqual([e["kind"] for e in reopened.events(task["id"])], ["created", "doing", "done"])
            reopened.set_state(task["id"], "planned")
            self.assertIsNone(reopened.get(task["id"])["completed_at"])
            self.assertEqual(len(reopened.events(task["id"])), 4)
            self.assertEqual(reopened.list(view="history"), [])

    def test_record_unplanned_work(self):
        task = self.store.add("Fixed a broken deploy", self.context, "done")
        self.assertIsNotNone(task["completed_at"])
        self.assertEqual([t["id"] for t in self.store.list(view="today")], [task["id"]])
        self.assertEqual([e["kind"] for e in self.store.events(task["id"])], ["created", "done"])

    def test_undo_restores_previous_completion_date(self):
        task = self.store.add("Completed earlier", self.context, "done")
        with self.store.db:
            self.store.db.execute("UPDATE tasks SET completed_at=? WHERE id=?",
                                  ("2026-01-01T12:00:00+00:00", task["id"]))
        before = self.store.get(task["id"])
        after = self.store.set_state(task["id"], "planned")
        restored = self.store.undo_state(before, after)
        self.assertEqual(restored["completed_at"], before["completed_at"])
        self.assertEqual(restored["state"], "done")
        self.assertEqual(self.store.events(task["id"])[-1]["kind"], "undo")

    def test_undo_does_not_overwrite_later_changes(self):
        before = self.store.add("Shared task", self.context)
        after = self.store.set_state(before["id"], "done")
        with closing(Store(self.path)) as other:
            other.edit(before["id"], note="Changed in another session")
        with self.assertRaisesRegex(ValueError, "changed since"):
            self.store.undo_state(before, after)
        self.assertEqual(self.store.get(before["id"])["state"], "done")
        self.assertEqual(self.store.get(before["id"])["note"], "Changed in another session")

    def test_editor_saves_title_note_and_status_together(self):
        task = self.store.add("Draft", self.context)
        saved = self.store.edit(task["id"], "Ready", "Checked the details", state="done",
                                expected_updated_at=task["updated_at"])
        self.assertEqual((saved["title"], saved["note"], saved["state"]), ("Ready", "Checked the details", "done"))
        self.assertEqual(saved["completed_at"], saved["updated_at"])
        self.assertEqual([e["kind"] for e in self.store.events(task["id"])], ["created", "edited", "done"])
        self.store.edit(task["id"], "Ready", "Checked the details", state="done")
        self.assertEqual(len(self.store.events(task["id"])), 3)

    def test_invalid_editor_save_does_not_partially_write(self):
        task = self.store.add("Keep this", self.context)
        with self.assertRaises(ValueError):
            self.store.edit(task["id"], "Bad save", "Do not persist", state="invalid")
        self.assertEqual(self.store.get(task["id"]), task)

    def test_editor_does_not_overwrite_a_concurrent_change(self):
        task = self.store.add("Shared", self.context)
        with closing(Store(self.path)) as other:
            other.edit(task["id"], note="Updated elsewhere")
        with self.assertRaisesRegex(ValueError, "changed elsewhere"):
            self.store.edit(task["id"], "Old draft", "Stale note", state="done", expected_updated_at=task["updated_at"])
        saved = self.store.get(task["id"])
        self.assertEqual((saved["title"], saved["note"], saved["state"]), ("Shared", "Updated elsewhere", "planned"))

    def test_projects_survive_workspace_ids_and_labels(self):
        self.store.add("Old workspace", self.context)
        new = Context(self.context.project, "new name", "w99", "w99:p2")
        self.store.add("New workspace", new)
        self.store.add("Other project", Context("/projects/s3", "s3"))
        self.assertEqual(len(self.store.list(new.project)), 2)
        self.assertEqual(len(self.store.list()), 3)

    def test_space_is_optional_and_does_not_move_the_task(self):
        task = self.store.add("Deploy", self.context)
        self.assertIsNone(task["space"])
        space = {"workspace_id": "w8", "label": "Release", "session": self.context.session}
        linked = self.store.edit(task["id"], space=space)
        self.assertEqual(linked["project"], task["project"])
        self.assertEqual(linked["workspace"], task["workspace"])
        with closing(Store(self.path)) as reopened:
            self.assertEqual(reopened.get(task["id"])["space"], space)
            self.assertEqual(reopened.list(search="Release")[0]["id"], task["id"])
            self.assertEqual(reopened.edit(task["id"], note="Ready")["space"], space)
            self.assertIsNone(reopened.edit(task["id"], space=None)["space"])
        self.assertEqual([e["kind"] for e in self.store.events(task["id"])],
                         ["created", "space_attached", "edited", "space_detached"])

    def test_add_with_space_and_invalid_link_are_atomic(self):
        space = {"workspace_id": "w8", "label": "Release", "session": self.context.session}
        task = self.store.add("Deploy", self.context, space=space)
        self.assertEqual(task["space"], space)
        with self.assertRaises(ValueError):
            self.store.add("Invalid", self.context, space={"workspace_id": "w1"})
        with self.assertRaises(ValueError):
            self.store.edit(task["id"], "Must not save", state="done", space={})
        self.assertEqual(self.store.get(task["id"]), task)
        self.assertEqual(len(self.store.list(view="all")), 1)

    def test_attached_tasks_are_visible_in_the_destination_space(self):
        space = {"workspace_id": "w8", "label": "Release", "session": self.context.session}
        task = self.store.add("Deploy", self.context, space=space)
        destination = "/projects/release"
        here = (self.context.session, "w8")
        self.assertEqual([t["id"] for t in self.store.list(destination, linked_here=here)], [task["id"]])
        self.assertEqual(self.store.list(destination, linked_here=("/tmp/other.sock", "w8")), [])
        self.assertEqual(self.store.list(destination, linked_here=(self.context.session, "w9")), [])
        self.store.set_state(task["id"], "done")
        self.assertEqual(self.store.list(destination, linked_here=here), [])
        self.assertEqual(len(self.store.list(destination, view="today", linked_here=here)), 1)
        self.store.edit(task["id"], space=None)
        self.assertEqual(self.store.list(destination, view="all", linked_here=here), [])
        self.assertEqual(len(self.store.list(self.context.project, view="all")), 1)

    def test_old_editor_cannot_overwrite_a_new_space_link(self):
        task = self.store.add("Shared", self.context)
        space = {"workspace_id": "w8", "label": "Release", "session": self.context.session}
        with closing(Store(self.path)) as other:
            other.edit(task["id"], space=space)
        with self.assertRaisesRegex(ValueError, "changed elsewhere"):
            self.store.edit(task["id"], note="Old draft", space=None, expected_updated_at=task["updated_at"])
        self.assertEqual(self.store.get(task["id"])["space"], space)

    def test_version_one_migration_preserves_existing_tasks_and_history(self):
        task = self.store.add("Existing completed task", self.context, "done", "Keep this note")
        events = self.store.events(task["id"])
        with self.store.db:
            self.store.db.execute("DROP TABLE task_spaces")
            self.store.db.execute("PRAGMA user_version=1")
        with closing(Store(self.path)) as upgraded:
            self.assertEqual(upgraded.get(task["id"]), task)
            self.assertEqual(upgraded.events(task["id"]), events)
            self.assertEqual(upgraded.db.execute("PRAGMA user_version").fetchone()[0], 2)
            self.assertEqual(upgraded.db.execute("PRAGMA integrity_check").fetchone()[0], "ok")

    def test_today_uses_local_calendar_date(self):
        yesterday = (datetime.now().astimezone().date() - timedelta(days=1)).isoformat()
        task = self.store.add("Yesterday's work", self.context, "done")
        with self.store.db:
            # Local noon converted to UTC must remain yesterday in the view.
            at = datetime.fromisoformat(yesterday + "T12:00:00").astimezone().astimezone(timezone.utc).isoformat()
            self.store.db.execute("UPDATE tasks SET completed_at=? WHERE id=?", (at, task["id"]))
        self.assertEqual(self.store.list(view="today"), [])
        self.assertEqual(len(self.store.list(view="history")), 1)

    def test_edit_preserves_completion_and_snapshot_titles(self):
        task = self.store.add("Original title", self.context, "done")
        edited = self.store.edit(task["id"], "Corrected title", "Useful detail")
        self.assertEqual(edited["completed_at"], task["completed_at"])
        self.assertEqual(self.store.events(task["id"])[1]["title"], "Original title")
        self.assertEqual(self.store.list(view="history", search="detail")[0]["id"], task["id"])

    def test_invalid_input_is_atomic(self):
        task = self.store.add("Valid title", self.context)
        for title in ("", " \n ", "x" * 501, "hidden\x1bcommand", "two\nlines"):
            with self.assertRaises(ValueError):
                self.store.edit(task["id"], title=title)
        with self.assertRaises(ValueError):
            self.store.set_state(task["id"], "invalid")
        self.assertEqual(self.store.get(task["id"])["title"], "Valid title")
        self.assertEqual(len(self.store.events(task["id"])), 1)

    def test_parallel_clients_keep_every_write(self):
        def write(i):
            with closing(Store(self.path)) as store:
                task = store.add(f"Concurrent task {i}", self.context)
                store.set_state(task["id"], "done")
        with ThreadPoolExecutor(max_workers=6) as pool:
            list(pool.map(write, range(24)))
        exported = self.store.export()
        self.assertEqual(len(exported["tasks"]), 24)
        self.assertEqual(len(exported["events"]), 48)
        self.assertEqual(self.store.db.execute("PRAGMA integrity_check").fetchone()[0], "ok")

    def test_newer_database_is_not_downgraded(self):
        with self.store.db:
            self.store.db.execute("PRAGMA user_version=3")
        with self.assertRaisesRegex(ValueError, "newer"):
            Store(self.path)
        self.assertEqual(self.store.db.execute("PRAGMA user_version").fetchone()[0], 3)

    def test_cli_roundtrip(self):
        command = [sys.executable, str(Path(__file__).parent / "todo.py"), "--db", str(self.path), "--project", self.temp.name]
        added = subprocess.run(command + ["add", "CLI task with 'quotes' and $symbols"], capture_output=True, text=True, check=True)
        task = json.loads(added.stdout)
        subprocess.run(command + ["done", str(task["id"])], capture_output=True, text=True, check=True)
        result = subprocess.run(command + ["list", "--view", "today"], capture_output=True, text=True, check=True)
        self.assertEqual(json.loads(result.stdout)[0]["state"], "done")
        error = subprocess.run(command + ["done", "999999"], capture_output=True, text=True)
        self.assertEqual(error.returncode, 1)
        self.assertIn("does not exist", error.stderr)


class SpaceTests(unittest.TestCase):
    def setUp(self):
        self.context = Context("/projects/infra", "infra", "w1", "w1:p1", "/tmp/current.sock")
        self.spaces = Spaces(self.context)
        self.link = {"workspace_id": "w2", "label": "Deploy", "session": self.context.session}

    def test_jump_resolves_current_label_and_exact_workspace_id(self):
        with patch.object(self.spaces, "command") as command:
            command.side_effect = [{"workspaces": [{"workspace_id": "w2", "label": "Renamed"}]}, {}]
            self.spaces.focus(self.link)
        self.assertEqual(command.call_args_list[-1].args, ("focus", "w2"))
        self.assertEqual(self.spaces.label(self.link), "Renamed")

    def test_closed_space_is_not_replaced_by_a_same_named_space(self):
        with patch.object(self.spaces, "command", return_value={"workspaces": [{"workspace_id": "w3", "label": "Deploy"}]}) as command:
            with self.assertRaisesRegex(ValueError, "closed"):
                self.spaces.focus(self.link)
        self.assertEqual(command.call_count, 1)

    def test_jump_does_not_cross_sessions(self):
        with patch.object(self.spaces, "command") as command:
            with self.assertRaisesRegex(ValueError, "another Herdr session"):
                self.spaces.focus(dict(self.link, session="/tmp/other.sock"))
        command.assert_not_called()

    def test_standalone_does_not_control_a_default_session(self):
        with patch.dict(os.environ, {}, clear=True), patch("herdr_todo.spaces.subprocess.run") as run:
            with self.assertRaisesRegex(ValueError, "inside Herdr"):
                self.spaces.refresh()
        run.assert_not_called()

    def test_commands_use_herdr_context_and_timeout(self):
        with patch.dict(os.environ, {"HERDR_ENV": "1", "HERDR_BIN_PATH": "/bin/herdr", "HERDR_SOCKET_PATH": "/tmp/other.sock"}, clear=True):
            with patch("herdr_todo.spaces.subprocess.run") as run:
                run.return_value = subprocess.CompletedProcess([], 0, json.dumps({"result": {"workspaces": [self.link]}}))
                self.assertEqual(self.spaces.refresh(), [self.link])
                self.assertEqual(run.call_args.args[0], ["/bin/herdr", "workspace", "list"])
                self.assertEqual(run.call_args.kwargs["env"]["HERDR_SOCKET_PATH"], self.context.session)
                self.assertEqual(run.call_args.kwargs["timeout"], 3)
                run.side_effect = subprocess.TimeoutExpired("herdr", 3)
                with self.assertRaisesRegex(ValueError, "reach Herdr"):
                    self.spaces.refresh()


class ContextTests(unittest.TestCase):
    def test_herdr_flat_context_prefers_workspace_root(self):
        env = {"HERDR_PLUGIN_ROOT": "/plugins/todo", "HERDR_SOCKET_PATH": "/tmp/test.sock",
               "HERDR_PLUGIN_CONTEXT_JSON": json.dumps({"workspace_cwd": "/projects/app",
                   "focused_pane_cwd": "/projects/app/src", "workspace_label": "App",
                   "workspace_id": "w4", "focused_pane_id": "w4:p3"})}
        with patch.dict(os.environ, env, clear=True):
            context = Context.current()
        self.assertEqual(context.project, "/projects/app")
        self.assertEqual(context.label, "App")
        self.assertEqual(context.pane, "w4:p3")

    def test_missing_plugin_context_is_not_source_directory(self):
        with patch.dict(os.environ, {"HERDR_PLUGIN_ROOT": "/plugins/todo"}, clear=True):
            with self.assertRaisesRegex(ValueError, "No project directory"):
                Context.current()
            self.assertEqual(Context.current("/projects/explicit").project, "/projects/explicit")

    def test_bad_context_reports_error(self):
        for raw in ("not json", "[]", "null"):
            with patch.dict(os.environ, {"HERDR_PLUGIN_CONTEXT_JSON": raw}, clear=True):
                with self.assertRaises(ValueError):
                    Context.current()

    def test_popup_preserves_context_and_uses_argv(self):
        context = Context("/projects/a b", "A", "w4", "w4:p3", "/tmp/test.sock")
        with patch.dict(os.environ, {"HERDR_BIN_PATH": "/bin/herdr", "HERDR_TODO_DB": "/tmp/tasks db"}, clear=True):
            with patch("herdr_todo.context.subprocess.run") as run:
                run.return_value.returncode = 0
                run.return_value.stdout = "{}"
                open_popup(context)
                argv = run.call_args.args[0]
        self.assertIn("HERDR_TODO_CONTEXT=" + context.json(), argv)
        self.assertIn("HERDR_TODO_DB=/tmp/tasks db", argv)
        self.assertNotIn("--target-pane", argv)
        self.assertNotIn("--workspace", argv)

    def test_standalone_and_plugin_use_same_default_database(self):
        with patch.dict(os.environ, {"XDG_STATE_HOME": "/state"}, clear=True):
            standalone = db_path()
        with patch.dict(os.environ, {"HERDR_PLUGIN_STATE_DIR": "/state/herdr/plugins/herdr-todo"}, clear=True):
            self.assertEqual(db_path(), standalone)


if __name__ == "__main__":
    unittest.main()
