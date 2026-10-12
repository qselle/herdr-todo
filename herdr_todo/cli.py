import argparse
from contextlib import closing
import json
import os
import sqlite3
import subprocess
import sys

from . import __version__
from .context import Context, open_popup
from .store import Store, db_path


def parser():
    root = argparse.ArgumentParser(description="Plan work and keep a completion journal in Herdr.")
    root.add_argument("--version", action="version", version=__version__)
    root.add_argument("--db", help="SQLite database (also HERDR_TODO_DB)")
    root.add_argument("--project", help="Project directory (default: current Herdr workspace or cwd)")
    commands = root.add_subparsers(dest="command")
    commands.add_parser("ui", help="Open the task TUI in this terminal")
    commands.add_parser("open", help="Open the installed plugin as a Herdr popup")
    commands.add_parser("path", help="Print the database path")
    add = commands.add_parser("add", help="Capture a task")
    add.add_argument("title")
    add.add_argument("--done", action="store_true", help="Record work already completed")
    add.add_argument("--note", default="")
    listing = commands.add_parser("list", help="List tasks as JSON")
    listing.add_argument("--all-projects", action="store_true")
    listing.add_argument("--view", choices=("active", "today", "history", "all"), default="active")
    listing.add_argument("--search", default="")
    for name, help_text in (("done", "Complete a task"), ("doing", "Start a task"), ("reopen", "Reopen a task")):
        command = commands.add_parser(name, help=help_text)
        command.add_argument("id", type=int)
    edit = commands.add_parser("edit", help="Edit a task")
    edit.add_argument("id", type=int)
    edit.add_argument("--title")
    edit.add_argument("--note")
    journal = commands.add_parser("journal", help="Show timestamped task events as JSON")
    journal.add_argument("--id", type=int)
    commands.add_parser("export", help="Export every project and its journal as JSON to stdout")
    return root


def main(argv=None):
    args = parser().parse_args(argv)
    command = args.command or "ui"
    try:
        if command == "path":
            print(db_path(args.db).resolve())
            return 0
        if command == "open":
            if args.db:
                os.environ["HERDR_TODO_DB"] = str(db_path(args.db).resolve())
            print(open_popup(Context.current(args.project)), end="")
            return 0
        if command == "ui" and not (sys.stdin.isatty() and sys.stdout.isatty()):
            raise ValueError("The task popup needs a terminal; use 'list' for JSON output")
        with closing(Store(db_path(args.db))) as store:
            if command == "ui":
                from .tui import run
                run(store, Context.current(args.project))
                return 0
            if command == "add":
                result = store.add(args.title, Context.current(args.project),
                                   "done" if args.done else "planned", args.note)
            elif command == "list":
                project = None if args.all_projects else Context.current(args.project).project
                result = store.list(project, args.view, args.search)
            elif command in ("done", "doing", "reopen"):
                result = store.set_state(args.id, "planned" if command == "reopen" else command)
            elif command == "edit":
                if args.title is None and args.note is None:
                    raise ValueError("Pass --title or --note")
                result = store.edit(args.id, args.title, args.note)
            elif command == "journal":
                result = store.events(args.id)
            else:
                result = store.export()
            print(json.dumps(result, ensure_ascii=False, indent=2))
        return 0
    except KeyboardInterrupt:
        return 130
    except (OSError, ValueError, TypeError, RuntimeError, sqlite3.Error, subprocess.SubprocessError) as exc:
        print(f"herdr-todo: {exc}", file=sys.stderr)
        return 1
    except ImportError as exc:
        print(f"herdr-todo: {exc}. Install Python with curses and SQLite support.", file=sys.stderr)
        return 1
