"""A task list with visible actions, keyboard navigation, and mouse controls."""
import curses
from datetime import datetime
import locale
import sqlite3
import time
import unicodedata

from .store import title_text
from .spaces import Spaces

VIEWS = ("active", "today", "history")
LABELS = {"active": "Tasks", "today": "Today", "history": "History"}
ICONS = {"planned": "[ ]", "doing": "[>]", "done": "[x]"}
ENTER = ("\n", "\r", curses.KEY_ENTER)
BACKSPACE = (curses.KEY_BACKSPACE, "\x7f", "\b")


def clean(text):
    return "".join(c if not unicodedata.category(c).startswith("C") else " " for c in str(text))


def width(text):
    return sum(0 if unicodedata.combining(c) else 2 if unicodedata.east_asian_width(c) in "WF" else 1 for c in text)


def clip(text, cells):
    result, used = [], 0
    for char in clean(text):
        size = width(char)
        if used + size > cells:
            break
        result.append(char)
        used += size
    return "".join(result)


def shorten(text, cells):
    text = clean(text)
    return text if width(text) <= cells else clip(text, max(0, cells - 1)) + ("…" if cells else "")


def wrap_cells(text, cells):
    """Word wrapping measured in terminal cells, including wide characters."""
    if cells < 1:
        return [""]
    lines, line = [], ""
    for word in clean(text).split():
        if line and width(line + " " + word) > cells:
            lines.append(line)
            line = ""
        while width(word) > cells:
            part = clip(word, cells)
            if not part:  # A wide character cannot fit a one-cell column.
                part = word[0]
            lines.append(part)
            word = word[len(part):]
        line = line + " " + word if line and word else line or word
    return lines + ([line] if line else []) or [""]


def local_time(stamp):
    return datetime.fromisoformat(stamp).astimezone().strftime("%Y-%m-%d %H:%M") if stamp else ""


class TextInput:
    """One logical line with a cursor and a horizontally scrolling viewport."""
    def __init__(self, value="", limit=500):
        self.value, self.cursor, self.limit = value, len(value), max(limit, len(value))

    def viewport(self, cells):
        start = self.cursor
        while start > 0 and width(self.value[start - 1:self.cursor]) < cells:
            start -= 1
        return clip(self.value[start:], cells), width(self.value[start:self.cursor]), start

    def click(self, cell, cells):
        text, _, start = self.viewport(cells)
        used, pos = 0, 0
        for char in text:
            if used + width(char) / 2 > cell:
                break
            used += width(char)
            pos += 1
        self.cursor = min(len(self.value), start + pos)

    def handle(self, key):
        if key in BACKSPACE and self.cursor:
            self.value = self.value[:self.cursor - 1] + self.value[self.cursor:]
            self.cursor -= 1
        elif key == curses.KEY_DC:
            self.value = self.value[:self.cursor] + self.value[self.cursor + 1:]
        elif key == curses.KEY_LEFT:
            self.cursor = max(0, self.cursor - 1)
        elif key == curses.KEY_RIGHT:
            self.cursor = min(len(self.value), self.cursor + 1)
        elif key in (curses.KEY_HOME, "\x01"):
            self.cursor = 0
        elif key in (curses.KEY_END, "\x05"):
            self.cursor = len(self.value)
        elif key == "\x15":
            self.value, self.cursor = "", 0
        elif key == "\x17":
            left = self.value[:self.cursor].rstrip()
            pos = left.rfind(" ") + 1
            self.value = self.value[:pos] + self.value[self.cursor:]
            self.cursor = pos
        elif isinstance(key, str) and key.isprintable() and len(self.value) < self.limit:
            self.value = self.value[:self.cursor] + key + self.value[self.cursor:]
            self.cursor += 1


class UI:
    def __init__(self, screen, store, context, spaces=None):
        self.screen, self.store, self.context = screen, store, context
        self.spaces = spaces if spaces is not None else Spaces(context)
        self.view, self.all_projects, self.search = "active", False, ""
        self.selected, self.selected_id, self.offset = 0, None, 0
        self.message, self.message_until = "", 0
        self.colors, self.hits, self.undo = {}, [], None
        self.mode, self.input, self.input_error = None, None, ""
        self.search_before, self.bookmarks, self.last_click = "", {}, (None, 0)
        self.search_position = (0, None, 0)
        self.input_space = None

    def notify(self, text):
        self.message, self.message_until = text, time.monotonic() + 5

    def put(self, y, x, text, attr=0):
        h, w = self.screen.getmaxyx()
        if 0 <= y < h and 0 <= x < w - 1:
            try:
                self.screen.addstr(y, x, clip(text, w - x - 1), attr)
            except curses.error:
                pass

    def hit(self, y, x, length, action, payload=None):
        self.hits.append((y, x, x + length, action, payload))

    def button(self, y, x, label, action, payload=None, active=False, focused=False):
        text = f" {label} "
        attr = curses.A_REVERSE if active else self.colors.get("accent", 0) | curses.A_BOLD
        if focused:
            attr |= curses.A_UNDERLINE
        self.put(y, x, text, attr)
        self.hit(y, x, width(text), action, payload)
        return x + width(text) + 1

    def mouse(self):
        try:
            _, x, y, _, state = curses.getmouse()
        except curses.error:
            return None
        if state & getattr(curses, "BUTTON4_PRESSED", 0):
            return ("up", None)
        if state & getattr(curses, "BUTTON5_PRESSED", 0):
            return ("down", None)
        flags = curses.BUTTON1_PRESSED | curses.BUTTON1_CLICKED | curses.BUTTON1_DOUBLE_CLICKED
        if state & flags:
            for row, left, right, action, payload in reversed(self.hits):
                if y == row and left <= x < right:
                    if action == "text":
                        return action, (payload, x - left)
                    return action, payload
        return None

    def read(self):
        try:
            return self.screen.get_wch()
        except curses.error:
            return None

    def cursor(self, visible):
        try:
            curses.curs_set(1 if visible else 0)
        except curses.error:
            pass

    def rows(self, view=None):
        linked_here = (self.context.session, self.context.workspace) if self.context.session and self.context.workspace else None
        tasks = self.store.list(None if self.all_projects else self.context.project, view or self.view, linked_here=linked_here)
        if not self.search:
            return tasks
        query = self.search.casefold()
        return [task for task in tasks if any(query in text.casefold() for text in
                (task["title"], task["note"], self.spaces.label(task["space"]) if task["space"] else ""))]

    def select(self, tasks, index):
        self.selected = min(max(index, 0), max(0, len(tasks) - 1))
        self.selected_id = tasks[self.selected]["id"] if tasks else None

    def switch(self, view, scope=None):
        self.bookmarks[(self.view, self.all_projects, self.search)] = (self.selected, self.selected_id, self.offset)
        self.view = view
        if scope is not None:
            self.all_projects = scope
        self.selected, self.selected_id, self.offset = self.bookmarks.get((self.view, self.all_projects, self.search), (0, None, 0))

    def draw(self, tasks, refresh=True):
        s = self.screen
        s.erase()
        self.hits = []
        h, w = s.getmaxyx()
        if h < 12 or w < 36:
            self.cursor(False)
            self.put(0, 0, "More room needed (36 x 12).")
            self.put(1, 0, "Resize to continue. Esc closes.")
            if refresh:
                s.refresh()
            return
        if self.selected_id is not None:
            match = next((i for i, t in enumerate(tasks) if t["id"] == self.selected_id), None)
            if match is not None:
                self.selected = match
        self.select(tasks, self.selected)
        scope = "All projects" if self.all_projects else self.context.label
        self.button(0, 1, shorten(scope, w - 17) + " ▾", "scope")
        self.button(0, w - 12, "Esc Close", "close")
        counts = {view: len(self.rows(view)) for view in ("active", "today")}
        x = 1
        for view in VIEWS:
            count = ("99+" if w < 44 and counts[view] > 99 else str(counts[view])) if view in counts else ""
            label = LABELS[view] + (" " + count if count else "")
            x = self.button(1, x, label, "view", view, active=view == self.view)
        x = self.button(2, 1, "+ New [a]", "add")
        x = self.button(2, x, "Find [/]", "search")
        self.button(2, x, "More [m]", "more")
        top, bottom = 4, h - 2
        cursor_at = None
        if self.mode:
            label = {"add": "+ ", "record": "✓ ", "search": "/ "}[self.mode]
            self.put(3, 1, label, self.colors.get("accent", 0))
            available = w - 6
            text, pos, _ = self.input.viewport(available)
            self.put(3, 3, " " * available, curses.A_REVERSE)
            self.put(3, 3, text, curses.A_REVERSE)
            if not text:
                placeholder = "Find a task…" if self.mode == "search" else "What did you finish?" if self.mode == "record" else "What needs doing?"
                self.put(3, 3, shorten(placeholder, available), curses.A_REVERSE | curses.A_DIM)
            self.hit(3, 3, available, "text", "inline")
            cursor_at = (3, 3 + pos)
            top = 5
        elif self.search:
            self.put(3, 2, shorten("Matching: " + self.search, w - 14), curses.A_DIM)
            self.button(3, w - 11, "× Clear", "clear")
            top = 5
        detail_x = max(50, w * 3 // 5) if w >= 94 and h >= 18 else None
        list_width = detail_x - 1 if detail_x else w
        if not tasks:
            title, hint = {
                "active": ("A little room for your next idea.", "Add a task when you're ready."),
                "today": ("A fresh start today.", "Finished something? Record it below."),
                "history": ("Your finished work lives here.", "Complete a task to start your history."),
            }[self.view]
            if self.search:
                title, hint = "No matching tasks.", "Try another word or clear the search."
            line_y = top + (1 if h >= 18 else 0)
            for line in wrap_cells(title, list_width - 4):
                if line_y < bottom:
                    self.put(line_y, 2, line, curses.A_BOLD)
                    line_y += 1
            if line_y + 1 < bottom:
                self.put(line_y + 1, 2, shorten(hint, list_width - 4), curses.A_DIM)
            if line_y + 3 < bottom and not self.mode:
                action = "clear" if self.search else "record" if self.view == "today" else "add"
                label = "Clear search" if self.search else "+ Record finished work" if self.view == "today" else "+ Add a task"
                self.button(line_y + 3, 2, label, action, active=True)
        else:
            lines, starts, ends, group = [], {}, {}, None
            for i, task in enumerate(tasks):
                section = ("IN PROGRESS" if task["state"] == "doing" else "UP NEXT") if self.view == "active" else local_time(task["completed_at"])[:10]
                if section != group:
                    lines.append(("heading", None, section))
                    group = section
                starts[i] = len(lines)
                title_lines = wrap_cells(task["title"], list_width - 9)
                max_lines = 3 if i == self.selected else 2
                if len(title_lines) > max_lines:
                    title_lines = title_lines[:max_lines]
                    title_lines[-1] = shorten(title_lines[-1] + " …", list_width - 9)
                for n, line in enumerate(title_lines):
                    lines.append(("task" if n == 0 else "continuation", i, line))
                if task["space"]:
                    lines.append(("space", i, shorten("↗ " + self.spaces.label(task["space"]), list_width - 9)))
                meta = []
                if self.all_projects:
                    meta.append(task["project_label"])
                if task["completed_at"]:
                    meta.append(local_time(task["completed_at"])[11:])
                if task["note"] and i == self.selected and not detail_x:
                    meta.append(task["note"])
                if meta and h >= 18:
                    lines.append(("meta", i, shorten(" · ".join(meta), list_width - 9)))
                ends[i] = len(lines)
                if h >= 26:
                    lines.append(("gap", i, ""))
            visible = max(1, bottom - top)
            start, end = starts[self.selected], ends[self.selected]
            self.offset = min(self.offset, start)
            if end > self.offset + visible:
                self.offset = min(start, max(0, end - visible))
            self.offset = min(self.offset, max(0, len(lines) - visible))
            for row, (kind, index, text) in enumerate(lines[self.offset:self.offset + visible], top):
                if kind == "heading":
                    self.put(row, 2, text, curses.A_DIM)
                elif kind != "gap":
                    task = tasks[index]
                    attr = curses.A_REVERSE if index == self.selected else self.colors.get(task["state"], 0)
                    self.put(row, 1, " " * (list_width - 3), attr)
                    self.put(row, 7, text, attr | (curses.A_DIM if kind == "meta" else 0))
                    self.hit(row, 1, list_width - 3, "select", index)
                    if kind == "space":
                        self.hit(row, 7, width(text), "go", index)
                    if kind == "task":
                        self.put(row, 2, ICONS[task["state"]], attr)
                        self.hit(row, 2, 3, "toggle", index)
            if len(lines) > visible:
                thumb = top + round(self.offset / max(1, len(lines) - visible) * (visible - 1))
                for row in range(top, bottom):
                    self.put(row, list_width - 2, "┃" if row == thumb else "│", curses.A_DIM)
        if detail_x and tasks:
            self.draw_preview(tasks[self.selected], top, bottom, detail_x, w - detail_x - 3)
        if self.mode:
            x = self.button(h - 2, 1, "↵ " + ("Done" if self.mode == "search" else "Add"), "input_save")
            if self.mode != "search":
                x = self.button(h - 2, x, "Tab Space", "input_space")
            self.button(h - 2, x, "Esc Cancel", "input_cancel")
            hint = "↓ to results · live search" if self.mode == "search" else "Space: " + self.spaces.label(self.input_space) + " (optional)"
            self.put(h - 1, 2, shorten(self.input_error or hint, w - 4), self.colors.get("accent", 0) if self.input_error else curses.A_DIM)
        else:
            if tasks:
                task = tasks[self.selected]
                x = self.button(h - 2, 1, "Enter Edit", "task", self.selected)
                self.button(h - 2, x, "Space " + ("Reopen" if task["state"] == "done" else "Done"), "toggle", self.selected)
                if w >= 44:
                    end = self.button(h - 2, x + 16, "l Space", "link")
                    if w >= 64:
                        end = self.button(h - 2, end, "s " + ("Plan" if task["state"] == "doing" else "Start"), "start")
                    if w >= 78 and task["space"]:
                        self.button(h - 2, end, "g Open space", "go")
            else:
                action = "clear" if self.search else "record" if self.view == "today" else "add"
                label = "Clear search" if self.search else "Record finished work" if self.view == "today" else "+ New task"
                self.button(h - 2, 1, label, action)
            default = f"{self.selected + 1} / {len(tasks)} · ↑↓ · " + ("g Open space" if tasks[self.selected]["space"] else "l Link space") if tasks else "Your plans and progress, in one place."
            message = self.message if time.monotonic() < self.message_until else default
            self.put(h - 1, 2, shorten(message, w - (16 if self.undo else 4)), curses.A_DIM)
            if self.undo:
                self.button(h - 1, w - 12, "u Undo", "undo")
        if refresh:
            self.cursor(bool(cursor_at))
            if cursor_at:
                s.move(*cursor_at)
            s.refresh()

    def draw_preview(self, task, top, bottom, x, available):
        for row in range(top, bottom):
            self.put(row, x, "│", curses.A_DIM)
        x += 2
        y = top
        for line in wrap_cells(task["title"], available)[:3]:
            self.put(y, x, line, curses.A_BOLD)
            y += 1
        status = {"planned": "Up next", "doing": "In progress", "done": "Completed"}[task["state"]]
        self.put(y + 1, x, status, self.colors.get(task["state"], 0))
        self.put(y + 2, x, shorten(task["project_label"], available), curses.A_DIM)
        y += 4
        if task["space"]:
            self.button(y, x, shorten("↗ " + self.spaces.label(task["space"]), available - 2), "go")
            y += 2
        for line in wrap_cells(task["note"] or "Add a note when you open this task.", available):
            if y >= bottom - 4:
                break
            self.put(y, x, line, 0 if task["note"] else curses.A_DIM)
            y += 1
        if y + 2 < bottom:
            label = "Completed " + local_time(task["completed_at"]) if task["completed_at"] else "Added " + local_time(task["created_at"])
            self.put(bottom - 3, x, shorten(label, available), curses.A_DIM)
            self.button(bottom - 1, x, "Open task ↵", "task", self.selected)

    def panel(self, title, height, max_width=66):
        self.draw(self.rows(), refresh=False)
        self.hits = []
        h, w = self.screen.getmaxyx()
        box_w, box_h = min(max_width, w - 2), min(height, h - 2)
        y, x = (h - box_h) // 2, (w - box_w) // 2
        for row in range(y, y + box_h):
            self.put(row, x, " " * box_w)
        self.put(y, x, "┌" + "─" * (box_w - 2) + "┐", self.colors.get("accent", 0))
        self.put(y + box_h - 1, x, "└" + "─" * (box_w - 2) + "┘", self.colors.get("accent", 0))
        for row in range(y + 1, y + box_h - 1):
            self.put(row, x, "│")
            self.put(row, x + box_w - 1, "│")
        self.put(y, x + 2, " " + shorten(title, box_w - 6) + " ", curses.A_BOLD)
        return y, x, box_h, box_w

    def menu(self, title, options, subtitle="", initial=0, searchable=False):
        selected, query = initial, ""
        while True:
            h, w = self.screen.getmaxyx()
            if h < 12 or w < 36:
                return None
            y, x, bh, bw = self.panel(title, len(options) + 6)
            shown = [option for option in options if query.casefold() in option[0].casefold()]
            selected = min(selected, max(0, len(shown) - 1))
            hint = "Find: " + query if query else subtitle or ("Type to find · ↑↓ Enter" if searchable else "↑ ↓ choose · Enter select")
            self.put(y + 1, x + 2, shorten(hint, bw - 4), curses.A_DIM)
            available = bh - 4
            start = max(0, selected - available + 1)
            for index in range(start, min(len(shown), start + available)):
                row = y + 2 + index - start
                self.button(row, x + 2, shorten(shown[index][0], bw - 6), "choose", index, active=index == selected)
            if not shown:
                self.put(y + 2, x + 3, "No matching spaces", curses.A_DIM)
            end = self.button(y + bh - 2, x + 2, "Esc Back", "back")
            self.put(y + bh - 2, end, "↑↓ Enter select", curses.A_DIM)
            self.screen.refresh()
            key = self.read()
            event = self.mouse() if key == curses.KEY_MOUSE else None
            if key == "\x1b" or key == "q" and not searchable or event and event[0] == "back":
                return None
            if event and event[0] == "choose":
                return shown[event[1]][1]
            if key in ENTER and shown:
                return shown[selected][1]
            if key in (curses.KEY_DOWN, "\t") or not searchable and key == "j" or event and event[0] == "down":
                selected = (selected + 1) % max(1, len(shown))
            elif key in (curses.KEY_UP, curses.KEY_BTAB) or not searchable and key == "k" or event and event[0] == "up":
                selected = (selected - 1) % max(1, len(shown))
            elif searchable and key in BACKSPACE:
                query, selected = query[:-1], 0
            elif searchable and isinstance(key, str) and key.isprintable():
                query, selected = query + key, 0

    def choose_space(self, current):
        self.cursor(False)
        error = ""
        try:
            spaces = self.spaces.refresh()
        except ValueError as exc:
            spaces, error = [], str(exc)
        spaces.sort(key=lambda item: (item["workspace_id"] != self.context.workspace, item["label"].casefold()))
        options = [("No space · optional", False)]
        initial = 0
        target = current["workspace_id"] if current and current["session"] == self.context.session else self.context.workspace if not current else None
        for space in spaces:
            suffix = " · here" if space["workspace_id"] == self.context.workspace else ""
            if sum(item["label"] == space["label"] for item in spaces) > 1:
                suffix += " · " + space["workspace_id"]
            if space["workspace_id"] == target:
                initial = len(options)
            options.append((space["label"] + suffix, space))
        if current and not any(space == current for _, space in options):
            # Keep an unavailable or renamed attachment unless the user explicitly replaces it.
            if current["session"] != self.context.session or not any(space["workspace_id"] == current["workspace_id"] for space in spaces):
                options.insert(1, ("Keep " + current["label"] + " · unavailable here", current))
                initial = 1
        choice = self.menu("Attach a space", options, subtitle=error, initial=initial, searchable=True)
        return (False, current) if choice is None else (True, choice or None)

    def begin_input(self, mode):
        self.mode, self.input_error = mode, ""
        self.search_before = self.search
        self.search_position = (self.selected, self.selected_id, self.offset)
        self.input = TextInput(self.search if mode == "search" else "")
        self.input_space = None

    def handle_input(self, key):
        event = self.mouse() if key == curses.KEY_MOUSE else None
        if event and event[0] == "text":
            self.input.click(event[1][1], self.screen.getmaxyx()[1] - 6)
            return
        if key in ("\x1b", "\x03") or event and event[0] in ("input_cancel", "close"):
            if self.mode == "search":
                self.search = self.search_before
                self.selected, self.selected_id, self.offset = self.search_position
            self.mode = None
            return
        if self.mode != "search" and (key == "\t" or event and event[0] == "input_space"):
            _, self.input_space = self.choose_space(self.input_space)
            return
        if event and event[0] != "input_save":
            if self.mode == "search" or not self.input.value.strip():
                self.mode = None
                return self.dispatch(*event, self.rows())
            self.input_error = "Add this task or cancel to leave."
            return
        save = key in ENTER or event and event[0] == "input_save"
        select_result = self.mode == "search" and (key == curses.KEY_DOWN or event and event[0] == "select")
        if save or select_result:
            if self.mode == "search":
                self.mode = None
                self.select(self.rows(), event[1] if event and event[0] == "select" else 0)
            else:
                try:
                    title = title_text(self.input.value)
                    new = self.store.add(title, self.context, "done" if self.mode == "record" else "planned", space=self.input_space)
                    target_view = "today" if self.mode == "record" else "active"
                    self.search, self.mode = "", None
                    self.switch(target_view)
                    self.selected_id = new["id"]
                    self.notify("Added to " + self.context.label)
                except (ValueError, sqlite3.Error) as exc:
                    self.input_error = str(exc)
            return
        self.input.handle(key)
        if self.mode == "search":
            self.search = self.input.value
            self.selected, self.selected_id, self.offset = 0, None, 0
        if key is not None and key != curses.KEY_MOUSE:
            self.input_error = ""

    def edit_task(self, task, initial_field="title"):
        # Keep the draft local until Save; Escape never commits partial edits.
        fields = {"title": TextInput(task["title"]), "note": TextInput(task["note"], 2000)}
        state, focus, error = task["state"], initial_field, ""
        space = task["space"]
        order = ("title", "note", "planned", "doing", "done", "space", "save", "cancel")
        try:
            while True:
                h, w = self.screen.getmaxyx()
                if h < 12 or w < 36:
                    self.draw(self.rows())
                    key = self.read()
                    if key in ("\x1b", "\x03"):
                        return
                    continue
                y, x, bh, bw = self.panel("Edit task", 15, max_width=76)
                compact = bh < 13
                title_y, note_y = y + 2, y + (4 if compact else 5)
                state_y = y + (5 if compact else 8)
                space_y = y + (6 if compact else 10)
                available = bw - 6
                cursor_at = None
                for name, row, label in (("title", title_y, "Title"), ("note", note_y, "Note (optional)")):
                    self.put(row - 1, x + 3, label, curses.A_DIM)
                    text, cursor, _ = fields[name].viewport(available)
                    attr = curses.A_REVERSE if focus == name else curses.A_UNDERLINE
                    self.put(row, x + 3, " " * available, attr)
                    self.put(row, x + 3, text, attr)
                    self.hit(row, x + 3, available, "text", name)
                    if focus == name:
                        cursor_at = (row, x + 3 + cursor)
                status_x = x + 2
                for value, label in (("planned", "Planned"), ("doing", "Doing"), ("done", "Done")):
                    status_x = self.button(state_y, status_x, label, "status", value, active=state == value, focused=focus == value)
                self.button(space_y, x + 2, shorten("Space: " + self.spaces.label(space) + " ▾", bw - 6), "space", active=focus == "space")
                hint = "Space picks · Enter saves" if focus in ("planned", "doing", "done") else "Tab fields · Enter saves"
                if focus == "space":
                    hint = "Space or Enter to choose"
                if not compact:
                    self.put(y + bh - 4, x + 3, shorten(error or hint, bw - 6), self.colors.get("accent", 0) if error else curses.A_DIM)
                elif error:
                    self.put(space_y + 1, x + 2, shorten(error, bw - 4), self.colors.get("accent", 0))
                else:
                    self.put(space_y + 1, x + 3, hint, curses.A_DIM)
                end = self.button(y + bh - 2, x + 2, "Enter Save", "save", active=focus == "save")
                self.button(y + bh - 2, end, "Esc Cancel", "cancel", active=focus == "cancel")
                self.cursor(bool(cursor_at))
                if cursor_at:
                    self.screen.move(*cursor_at)
                self.screen.refresh()
                key = self.read()
                event = self.mouse() if key == curses.KEY_MOUSE else None
                if key in ("\x1b", "\x03") or event and event[0] == "cancel" or key in ENTER and focus == "cancel":
                    return
                if event and event[0] == "text":
                    focus, cell = event[1]
                    fields[focus].click(cell, available)
                elif event and event[0] == "status":
                    state, focus = event[1], event[1]
                elif event and event[0] == "space" or focus == "space" and key in (*ENTER, " "):
                    chosen, space = self.choose_space(space)
                    focus = "save" if chosen else "space"
                elif key in ("\t", curses.KEY_BTAB):
                    step = -1 if key == curses.KEY_BTAB else 1
                    focus = order[(order.index(focus) + step) % len(order)]
                elif key in ENTER or event and event[0] == "save":
                    try:
                        after = self.store.edit(task["id"], fields["title"].value, fields["note"].value, state=state, expected_updated_at=task["updated_at"], space=space)
                    except (ValueError, sqlite3.Error) as exc:
                        error = str(exc)
                        continue
                    if task["updated_at"] != after["updated_at"]:
                        self.undo = (task, after) if task["state"] != state else None
                    self.notify("Saved: " + after["title"])
                    return
                elif focus in fields:
                    fields[focus].handle(key)
                elif focus in ("planned", "doing", "done") and key == " ":
                    state = focus
                elif focus in ("planned", "doing", "done") and key in (curses.KEY_LEFT, curses.KEY_RIGHT):
                    states = ("planned", "doing", "done")
                    step = -1 if key == curses.KEY_LEFT else 1
                    state = focus = states[(states.index(focus) + step) % 3]
        finally:
            self.cursor(False)

    def page(self, title, lines):
        offset = 0
        while True:
            self.screen.erase()
            self.hits = []
            h, w = self.screen.getmaxyx()
            self.put(0, 2, shorten(title, w - 4), curses.A_BOLD)
            wrapped = []
            for line in lines:
                wrapped.extend(wrap_cells(line, max(1, w - 5)))
            visible = max(1, h - 4)
            offset = max(0, min(offset, len(wrapped) - visible))
            for y, line in enumerate(wrapped[offset:offset + visible], 2):
                self.put(y, 2, line)
            self.button(h - 1, 1, "Esc Back", "back")
            self.put(h - 1, 14, "↑ ↓ scroll", curses.A_DIM)
            self.screen.refresh()
            key = self.read()
            event = self.mouse() if key == curses.KEY_MOUSE else None
            if key in ("q", "\x1b", *ENTER) or event and event[0] == "back":
                return
            if key in ("j", curses.KEY_DOWN) or event and event[0] == "down":
                offset += 1
            elif key in ("k", curses.KEY_UP) or event and event[0] == "up":
                offset -= 1

    def change_state(self, task, state):
        before = self.store.get(task["id"])
        after = self.store.set_state(task["id"], state)
        self.undo = (before, after) if before["state"] != after["state"] else None
        verb = {"done": "Completed", "doing": "Started", "planned": "Moved to up next"}[state]
        self.notify(verb + ": " + after["title"])
        self.selected_id = task["id"]

    def dispatch(self, action, payload, tasks):
        index = payload if action in ("task", "select", "toggle", "go") and payload is not None else self.selected
        task = tasks[min(index, len(tasks) - 1)] if tasks else None
        if action == "close":
            if self.search:
                self.search = ""
                self.offset = 0
            else:
                return False
        elif action == "view":
            self.switch(payload)
        elif action == "scope":
            choice = self.menu("Show tasks from", [(self.context.label + " + tasks attached here", "current"), ("All projects on this machine", "all")])
            if choice:
                self.switch(self.view, scope=choice == "all")
        elif action in ("up", "down"):
            self.select(tasks, self.selected + (-1 if action == "up" else 1))
        elif action == "clear":
            self.search, self.offset = "", 0
        elif action in ("search", "add", "record"):
            self.begin_input(action)
        elif action == "toggle" and task:
            self.change_state(task, "planned" if task["state"] == "done" else "done")
        elif action == "start" and task:
            self.change_state(task, "planned" if task["state"] == "doing" else "doing")
        elif action == "link" and task:
            before = self.store.get(task["id"])
            chosen, space = self.choose_space(before["space"])
            if chosen:
                after = self.store.edit(task["id"], space=space, expected_updated_at=before["updated_at"])
                if before["updated_at"] != after["updated_at"]:
                    self.undo = None
                self.notify("Space: " + self.spaces.label(space))
        elif action == "go" and task:
            if task["space"]:
                self.spaces.focus(task["space"])
                return False
            return self.dispatch("link", None, tasks)
        elif action == "undo" and self.undo:
            before, after = self.undo
            try:
                self.store.undo_state(before, after)
            finally:
                self.undo = None
            self.switch("history" if before["state"] == "done" else "active")
            self.selected_id = before["id"]
            self.notify("Undone: " + before["title"])
        elif action == "select" and task:
            self.select(tasks, index)
            previous_id, previous_time = self.last_click
            self.last_click = (task["id"], time.monotonic())
            if previous_id == task["id"] and self.last_click[1] - previous_time < .4:
                self.edit_task(self.store.get(task["id"]))
                self.last_click = (None, 0)
        elif action in ("task", "edit", "note") and task:
            self.select(tasks, index)
            self.edit_task(self.store.get(task["id"]), "note" if action == "note" else "title")
        elif action == "details" and task:
            events = [f"{local_time(e['at'])}  {e['kind']}  {e['title']}" for e in self.store.events(task["id"])]
            self.page(task["title"], ["Status: " + task["state"], "Project: " + task["project_label"], "Space: " + self.spaces.label(task["space"]), "", task["note"] or "No note yet.", "", "Activity", *events])
        elif action == "more":
            options = [("Record work already finished", "record"), ("Choose project view", "scope"), ("Help & shortcuts", "help"), ("Storage information", "info")]
            if task:
                options.insert(0, ("Selected task activity", "details"))
                options.insert(0, ("Attach or change space", "link"))
                if task["space"]:
                    options.insert(0, ("Open attached space", "go"))
            if self.undo:
                options.insert(0, ("Undo last status change", "undo"))
            choice = self.menu("More actions", options)
            if choice:
                return self.dispatch(choice, None, tasks)
        elif action == "help":
            self.page("Getting around", [
                "New adds a task inline. Find filters the list as you type.",
                "Click once to select; double-click or Enter to edit. The editor saves title, note and status together.",
                "Click a checkbox to complete a task. Undo restores its previous status.",
                "Spaces are optional: Tab while adding or l in the list opens a searchable picker. Click the space under a task or press g to open it.",
                "Attached tasks also appear when you open Todo in that space. They stay in their original project too. Choose No space to remove the link.",
                "Tabs remember your position for this popup. Wide windows show a preview beside the list.", "",
                "Keyboard shortcuts", "a / A   New task / Record finished work", "Enter / e   Edit selected task", "n   Edit note", "Space   Complete / Reopen", "s   Start / Move to up next", "l / g   Attach space / Open space", "u   Undo", "/   Live search", "Tab, Left/Right, 1-3   Tasks / Today / History", "w   Project view", "d   Selected task activity", "?   Help", "Esc   Cancel input, clear search, then close", "",
                "In the editor: Tab changes fields; arrows or Space choose a focused status. Space or Enter opens the space picker. Save commits the whole draft; Escape cancels it.",
                "Today uses the local date. Reopened tasks keep earlier completions in their activity.",
            ])
        elif action == "info":
            self.page("Storage", ["Database", str(self.store.path), "", "Current project", self.context.project, "", "Tasks are stored locally on this machine and survive closing a workspace.", "", "Export from a checkout:", "python3 todo.py --db <database above> export > tasks.json"])
        return True

    def loop(self):
        s = self.screen
        s.keypad(True)
        s.timeout(500)
        self.cursor(False)
        if any(task["space"] for task in self.rows()):
            try:
                self.spaces.refresh()
            except ValueError:
                pass
        try:
            curses.mousemask(curses.ALL_MOUSE_EVENTS)
            curses.mouseinterval(0)
        except curses.error:
            pass
        if curses.has_colors():
            curses.start_color()
            background = curses.COLOR_BLACK
            try:
                curses.use_default_colors()
                background = -1
            except curses.error:
                pass
            for pair, (name, color) in enumerate((("accent", curses.COLOR_CYAN), ("doing", curses.COLOR_YELLOW), ("done", curses.COLOR_GREEN)), 1):
                curses.init_pair(pair, color, background)
                self.colors[name] = curses.color_pair(pair)
        shortcuts = {"a": "add", "A": "record", "s": "start", "e": "edit", "n": "note", "l": "link", "g": "go", "/": "search", "w": "scope", "?": "help", "i": "info", "u": "undo", "m": "more", "d": "details", "q": "close", "\x1b": "close"}
        while True:
            tasks = self.rows()
            self.draw(tasks)
            key = self.read()
            if key is None:
                continue
            h, w = s.getmaxyx()
            if (h < 12 or w < 36) and key not in ("q", "\x1b"):
                continue
            if self.mode:
                try:
                    if self.handle_input(key) is False:
                        return
                except (ValueError, sqlite3.Error, OSError) as exc:
                    self.notify(str(exc))
                continue
            event = None
            if key == curses.KEY_MOUSE:
                event = self.mouse()
            elif key in ENTER:
                event = ("task", self.selected) if tasks else ("clear" if self.search else "record" if self.view == "today" else "add", None)
            elif key == " ":
                event = ("toggle", self.selected)
            elif key in ("j", curses.KEY_DOWN, "k", curses.KEY_UP):
                event = ("down" if key in ("j", curses.KEY_DOWN) else "up", None)
            elif key in (curses.KEY_NPAGE, curses.KEY_PPAGE):
                self.select(tasks, self.selected + (1 if key == curses.KEY_NPAGE else -1) * max(1, (h - 6) // 2))
            elif key in ("\t", curses.KEY_RIGHT, curses.KEY_LEFT, curses.KEY_BTAB):
                step = -1 if key in (curses.KEY_LEFT, curses.KEY_BTAB) else 1
                event = ("view", VIEWS[(VIEWS.index(self.view) + step) % len(VIEWS)])
            elif key in ("1", "2", "3"):
                event = ("view", VIEWS[int(key) - 1])
            elif key in BACKSPACE:
                event = ("clear", None)
            elif key in shortcuts:
                event = (shortcuts[key], None)
            if event:
                try:
                    if self.dispatch(*event, tasks) is False:
                        return
                except (ValueError, sqlite3.Error, OSError) as exc:
                    self.notify(str(exc))


def run(store, context):
    locale.setlocale(locale.LC_ALL, "")
    if hasattr(curses, "set_escdelay"):
        curses.set_escdelay(25)
    curses.wrapper(lambda screen: UI(screen, store, context).loop())
