# Herdr Todo

Your plans and finished work, one shortcut away in [Herdr](https://herdr.dev).

Capture tasks without leaving the terminal, mark what you're working on, and look
back at what you finished today. Task history stays on disk when panes and
workspaces close.

```text
  infra ▾                                    Esc Close
  Tasks 3   Today 2   History
  + New [a]   Find [/]   More [m]

  IN PROGRESS
  [>]  Review storage sizing
       ↗ storage-lab
  UP NEXT
  [ ]  Document the rollout
  [ ]  Follow up on the registry change

  Enter Edit   Space Done   l Space   s Start
  1 / 3 · ↑↓ · g Open space
```

## Requirements

- Herdr 0.9.3 or newer.
- Linux or macOS with Python 3.10+ and its `curses` and `sqlite3` modules.
- Git for installation from GitHub.

No pip packages, build step, service, or account required. On macOS, install a
current Python 3 if needed. On Linux, use your distribution's Python package.
Verify the modules with `python3 -c 'import curses, sqlite3'`.

## Install

Install the first release from GitHub:

```sh
herdr plugin install qselle/herdr-todo --ref v0.1.0
```

For a local checkout:

```sh
herdr plugin link /absolute/path/to/herdr-todo
```

Add this binding to `~/.config/herdr/config.toml`, using an unused key if you
already have something on `prefix+t`:

```toml
[[keys.command]]
key = "prefix+t"
type = "plugin_action"
command = "herdr-todo.open"
description = "open tasks"
```

Reload configuration from Herdr's menu, or run `herdr server reload-config` from
the intended Herdr session. With the default prefix, press **Ctrl+B, then T**.
The popup closes with **q** or **Escape**, restoring your terminal layout.
While searching or editing, Escape first cancels input or clears the filter.

You can also open it from a Herdr pane:

```sh
herdr plugin action invoke herdr-todo.open
```

With SSH, install the plugin and Python on the machine running the Herdr server.
Put the binding in the configuration providing your active keybindings (normally
the client). Tasks live on that server; there is no cross-machine sync.

## Use

**Click New, type a task, and press Enter.** Adding and searching happen inline,
so your list stays visible. Find filters titles, notes, and attached space names
as you type; press Down or Enter to move into the results, or Escape to cancel
the search. You can also click a result's checkbox or space directly.

Click once to select a task. **Enter or a double-click opens its editor**, where
you can change the title, note, status, and optional space together. Tab moves
between fields, and arrows or Space choose a focused status. Enter saves the
draft, or opens the picker when the space field is focused. Escape cancels the
draft. Click a checkbox, or press Space in the list, to complete a task immediately.

**Attach a space when it helps you remember where the work is happening.**
While adding, press **Tab** or click **Tab Space**, type part of a space's name,
and choose it. Press Enter to add the task. For an existing task, press **l**,
click **l Space**, or use the space field in its editor. The current space is
the default picker choice; **No space** removes an attachment. Escape leaves
the previous choice intact. Adding an ordinary task still needs no space selection.

Attached spaces appear under tasks as **↗ space-name**. Click that link or press
**g** to close the popup and focus the space. The task also appears when you open
Todo there, while remaining in its original project. Links target existing
workspaces in the same Herdr session; closed spaces keep their saved label so
you can choose another one. Opening a link never creates or replaces a workspace.

Active work is grouped into **In Progress** and **Up Next**. Long titles wrap,
and the selected task stays selected when its position changes. Switch to
**Today** or **History** to revisit completed work. Each tab remembers your
position while the popup is open. Wider popups show the selected task's details
beside the list; smaller ones keep the focus on tasks.

Status changes show an **Undo** button. Undo restores the previous state and
completion date, and refuses to overwrite a task another session changed in
the meantime. To record work you already finished, choose **More → Record work
already finished**.

Buttons, tabs, the project selector, and task checkboxes accept mouse clicks;
the mouse wheel moves through tasks. Keyboard shortcuts remain available:

| Key | Action |
| --- | --- |
| `a` / `A` | Add a task / record finished work |
| `Enter` / `e` | Edit the selected task; Enter adds a task in an empty view |
| `Space` | Complete or reopen |
| `u` | Undo the last status change in this popup |
| `s` | Start working / move to Up Next |
| `n` | Open the editor at the note field |
| `l` / `g` | Attach or change a space / open the attached space |
| `Tab`, `←` / `→`, `1`–`3` | Tasks / Today / History |
| `w` | Current project and tasks attached here / all projects |
| `/` | Live search by title, note, or attached space |
| `Backspace` | Clear search |
| `↑` / `↓`, `j` / `k`, Page Up / Down | Select a task |
| `m` | More actions |
| `d` | Selected task's activity |
| `?` / `i` | Help / storage information |
| `Esc` / `q` | Clear a filter, or close the popup |

In an input field, arrows move the cursor, Home/End move to either end, Ctrl+U
clears the field, and Ctrl+W deletes the previous word. Empty or invalid titles
stay in the editor with an explanation, so you can correct them without starting
over. Resizing preserves an unfinished draft. The interface supports popups as
small as 36 columns by 12 rows.

New tasks belong to the project where you opened the popup, including when
viewing all projects. Project identity uses the workspace's absolute directory;
two workspaces opened on the same directory share tasks. Separate worktree
directories have separate lists. Moving a directory does not move its tasks;
they remain available in the all-projects view. An optional space attachment is
separate from project ownership. The current view includes the project's tasks
and tasks explicitly attached to this workspace in this session.

Today shows tasks **currently done** whose completion date is today in the
machine's local timezone. Reopening a task removes it from that view but retains
its earlier completion in the activity log. An agent becoming idle never marks
a task complete automatically.

## CLI

Run these from a checkout, or use the absolute path to `todo.py`:

```sh
python3 todo.py --project ~/work/infra add "Check storage sizing"
python3 todo.py --project ~/work/infra add "Fixed the deploy" --done
python3 todo.py doing 1
python3 todo.py done 1
python3 todo.py reopen 1
python3 todo.py edit 1 --note "Waiting for the hardware details"
python3 todo.py list --all-projects --view today
python3 todo.py journal --id 1
python3 todo.py export > tasks.json
```

CLI output is JSON. Outside Herdr, new tasks default to the current directory;
`--project` chooses another directory. Global `--db` and `--project` options go
**before** the subcommand. `ui` opens the interface in an ordinary terminal too.

## Storage and updates

The plugin stores `tasks.sqlite3` in `HERDR_PLUGIN_STATE_DIR`. For standalone use,
the default is `${XDG_STATE_HOME:-~/.local/state}/herdr/plugins/herdr-todo/tasks.sqlite3`,
matching Herdr's plugin state layout. `HERDR_TODO_DB` or `--db PATH` overrides it.
Press `i` in the popup or run `python3 todo.py path` to find the database.

SQLite transactions serialize concurrent updates from multiple sessions. Task
state changes retain timestamped events, and JSON exports include both current
tasks, optional space links, and the full journal. Existing databases upgrade
automatically; existing tasks begin with no explicit space attachment. Version 2
adds a separate attachment table and preserves existing tasks and activity.
Export to JSON for inspection, or use SQLite's
backup facility for a restorable database backup:

```sh
sqlite3 /path/to/tasks.sqlite3 ".backup '/path/to/tasks-backup.sqlite3'"
```

The database is separate from the installed source checkout. To upgrade a GitHub
installation, rerun `herdr plugin install qselle/herdr-todo --ref VERSION`.
Herdr replaces the source checkout; tasks stay in the state directory. To restore
a database backup, close task popups and stop task CLI writers, then replace the
database with the backup. JSON import is not part of this first release.

## Development

```sh
python3 -m unittest -v test_tasks
herdr plugin link /absolute/path/to/herdr-todo
```

The plugin uses only Python's standard library. Its manifest declares one
`open` action and one terminal popup. The UI, task store, CLI, and Herdr context
adapter are separate modules.

Keep the versions in `herdr-plugin.toml` and `herdr_todo/__init__.py` aligned
when tagging a release. Installation uses the tagged source directly, with no
build step. The `herdr-plugin` repository topic makes the plugin discoverable in
the [Herdr marketplace](https://herdr.dev/docs/marketplace/).

Herdr plugin docs: <https://herdr.dev/docs/plugins/>.
