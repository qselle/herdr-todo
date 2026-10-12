package main

import (
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/gdamore/tcell/v2"
)

var views = []string{"active", "today", "history"}
var viewLabels = map[string]string{"active": "Tasks", "today": "Today", "history": "History"}
var stateIcons = map[string]string{"planned": "[ ]", "doing": "[>]", "done": "[x]"}
var editorOrder = []string{"title", "note", "planned", "doing", "done", "space", "save", "cancel"}

type position struct {
	index  int
	id     int64
	offset int
}
type bookmark struct {
	view   string
	all    bool
	search string
}
type undoChange struct{ before, after Task }
type action struct {
	kind, value string
	index, cell int
}
type hit struct {
	y, left, right int
	action         action
}
type menuOption struct {
	label string
	value any
}
type menuState struct {
	title, subtitle, query string
	options, shown         []menuOption
	selected               int
	searchable             bool
	choose                 func(any) error
}
type editorState struct {
	task                Task
	title, note         *TextInput
	state, focus, error string
	space               *Space
}
type pageState struct {
	title  string
	lines  []string
	offset int
}

type UI struct {
	screen         tcell.Screen
	store          *Store
	context        Context
	spaces         SpaceSource
	view, search   string
	allProjects    bool
	position       position
	bookmarks      map[bookmark]position
	tasks          []Task
	counts         map[string]int
	hits           []hit
	message        string
	messageUntil   time.Time
	undo           *undoChange
	mode           string
	input          *TextInput
	inputError     string
	inputSpace     *Space
	searchBefore   string
	searchPosition position
	menu           *menuState
	editor         *editorState
	page           *pageState
	lastClickID    int64
	lastClickAt    time.Time
	buttons        tcell.ButtonMask
	pasting        bool
	paste          strings.Builder
	done           bool
}

func newUI(screen tcell.Screen, store *Store, c Context, spaces SpaceSource) *UI {
	if spaces == nil {
		spaces = &HerdrSpaces{context: c}
	}
	return &UI{screen: screen, store: store, context: c, spaces: spaces, view: "active", bookmarks: make(map[bookmark]position), counts: make(map[string]int)}
}

func (u *UI) notify(text string) { u.message, u.messageUntil = text, time.Now().Add(5*time.Second) }

func (u *UI) rows(view string) ([]Task, error) {
	opt := ListOptions{View: view, Session: u.context.Session, Workspace: u.context.Workspace}
	if !u.allProjects {
		opt.Project = &u.context.Project
	}
	tasks, err := u.store.List(opt)
	if err != nil {
		return nil, err
	}
	if u.search == "" {
		return tasks, nil
	}
	filtered := []Task{}
	for _, t := range tasks {
		label := ""
		if t.Space != nil {
			label = u.spaces.Label(t.Space)
		}
		if containsFold(t.Title, u.search) || containsFold(t.Note, u.search) || containsFold(label, u.search) {
			filtered = append(filtered, t)
		}
	}
	return filtered, nil
}

func (u *UI) refresh() error {
	for _, view := range views {
		tasks, err := u.rows(view)
		if err != nil {
			return err
		}
		u.counts[view] = len(tasks)
		if view == u.view {
			u.tasks = tasks
		}
	}
	for i, task := range u.tasks {
		if task.ID == u.position.id {
			u.position.index = i
			break
		}
	}
	u.selectTask(u.position.index)
	return nil
}

func (u *UI) selectTask(index int) {
	u.position.index = min(max(index, 0), max(0, len(u.tasks)-1))
	u.position.id = 0
	if len(u.tasks) > 0 {
		u.position.id = u.tasks[u.position.index].ID
	}
}

func (u *UI) switchView(view string, all bool) {
	u.bookmarks[bookmark{u.view, u.allProjects, u.search}] = u.position
	u.view, u.allProjects = view, all
	u.position = u.bookmarks[bookmark{view, all, u.search}]
}

func indexOf(values []string, value string) int {
	for i, v := range values {
		if value == v {
			return i
		}
	}
	return 0
}
func cycle(values []string, value string, step int) string {
	return values[(indexOf(values, value)+step+len(values))%len(values)]
}
func isRune(k *tcell.EventKey, r rune) bool {
	return k != nil && k.Key() == tcell.KeyRune && k.Rune() == r
}
func isKey(k *tcell.EventKey, keys ...tcell.Key) bool {
	if k != nil {
		for _, key := range keys {
			if k.Key() == key {
				return true
			}
		}
	}
	return false
}

func (u *UI) mouse(e *tcell.EventMouse) action {
	x, y := e.Position()
	buttons, previous := e.Buttons(), u.buttons
	u.buttons = buttons
	if buttons&tcell.WheelUp != 0 {
		return action{kind: "up", index: -1}
	}
	if buttons&tcell.WheelDown != 0 {
		return action{kind: "down", index: -1}
	}
	if buttons&tcell.Button1 == 0 || previous&tcell.Button1 != 0 {
		return action{}
	}
	for i := len(u.hits) - 1; i >= 0; i-- {
		h := u.hits[i]
		if y == h.y && x >= h.left && x < h.right {
			a := h.action
			a.cell = x - h.left
			return a
		}
	}
	return action{}
}

func (u *UI) handle(event tcell.Event) error {
	var k *tcell.EventKey
	a := action{index: -1}
	switch e := event.(type) {
	case *tcell.EventResize:
		u.screen.Sync()
		return nil
	case *tcell.EventInterrupt:
		if e.Data() == "quit" {
			u.done = true
		}
		return nil
	case *tcell.EventPaste:
		if e.Start() {
			u.pasting = true
			u.paste.Reset()
		} else {
			u.pasting = false
			u.insertPaste(u.paste.String())
			u.paste.Reset()
		}
		return nil
	case *tcell.EventKey:
		k = e
		if u.pasting {
			if k.Key() == tcell.KeyRune {
				u.paste.WriteRune(k.Rune())
			} else if isKey(k, tcell.KeyEnter, tcell.KeyTab) {
				u.paste.WriteByte(' ')
			}
			return nil
		}
	case *tcell.EventMouse:
		a = u.mouse(e)
	default:
		return nil
	}
	w, h := u.screen.Size()
	if (w < 36 || h < 12) && !isKey(k, tcell.KeyEscape, tcell.KeyCtrlC) && !(u.mode == "" && u.editor == nil && u.menu == nil && isRune(k, 'q')) {
		return nil
	}
	if u.menu != nil {
		return u.handleMenu(k, a)
	}
	if u.editor != nil {
		return u.handleEditor(k, a)
	}
	if u.page != nil {
		u.handlePage(k, a)
		return nil
	}
	if u.mode != "" {
		return u.handleInput(k, a)
	}
	if k != nil {
		switch {
		case isKey(k, tcell.KeyEnter):
			if len(u.tasks) > 0 {
				a.kind = "task"
			} else {
				a.kind = u.emptyAction()
			}
		case isRune(k, ' '):
			a.kind = "toggle"
		case isKey(k, tcell.KeyDown) || isRune(k, 'j'):
			a.kind = "down"
		case isKey(k, tcell.KeyUp) || isRune(k, 'k'):
			a.kind = "up"
		case isKey(k, tcell.KeyPgDn, tcell.KeyPgUp):
			step := max(1, (h-6)/2)
			if k.Key() == tcell.KeyPgUp {
				step = -step
			}
			u.selectTask(u.position.index + step)
		case isKey(k, tcell.KeyTab, tcell.KeyBacktab, tcell.KeyLeft, tcell.KeyRight):
			step := 1
			if isKey(k, tcell.KeyLeft, tcell.KeyBacktab) {
				step = -1
			}
			a.kind, a.value = "view", cycle(views, u.view, step)
		case isKey(k, tcell.KeyBackspace, tcell.KeyBackspace2):
			a.kind = "clear"
		case isKey(k, tcell.KeyEscape, tcell.KeyCtrlC):
			a.kind = "close"
		case k.Key() == tcell.KeyRune:
			if k.Rune() >= '1' && k.Rune() <= '3' {
				a.kind, a.value = "view", views[int(k.Rune()-'1')]
			} else {
				a.kind = map[rune]string{'a': "add", 'A': "record", 's': "start", 'e': "edit", 'n': "note", 'l': "link", 'g': "go", '/': "search", 'w': "scope", '?': "help", 'i': "info", 'u': "undo", 'm': "more", 'd': "details", 'q': "close"}[k.Rune()]
			}
		}
	}
	return u.dispatch(a)
}

func (u *UI) insertPaste(text string) {
	if u.menu != nil {
		if u.menu.searchable {
			u.menu.query += clean(text)
			u.menu.selected = 0
		}
	} else if e := u.editor; e != nil {
		if e.focus == "title" {
			e.title.insert(text)
		} else if e.focus == "note" {
			e.note.insert(text)
		}
	} else if u.mode != "" {
		u.input.insert(text)
		if u.mode == "search" {
			u.search = u.input.String()
			u.position = position{}
		}
	}
}

func (u *UI) emptyAction() string {
	if u.search != "" {
		return "clear"
	}
	if u.view == "today" {
		return "record"
	}
	return "add"
}

func (u *UI) beginInput(mode string) {
	u.mode, u.inputError = mode, ""
	u.searchBefore, u.searchPosition = u.search, u.position
	value := ""
	if mode == "search" {
		value = u.search
	}
	u.input, u.inputSpace = newInput(value, 500), nil
}

func (u *UI) handleInput(k *tcell.EventKey, a action) error {
	if a.kind == "text" {
		w, _ := u.screen.Size()
		u.input.click(a.cell, w-6)
		return nil
	}
	if isKey(k, tcell.KeyEscape, tcell.KeyCtrlC) || a.kind == "input_cancel" || a.kind == "close" {
		if u.mode == "search" {
			u.search, u.position = u.searchBefore, u.searchPosition
		}
		u.mode = ""
		return nil
	}
	if u.mode != "search" && (isKey(k, tcell.KeyTab) || a.kind == "input_space") {
		u.chooseSpace(u.inputSpace, func(space *Space) error { u.inputSpace = space; return nil })
		return nil
	}
	if a.kind != "" && a.kind != "input_save" {
		if u.mode == "search" || strings.TrimSpace(u.input.String()) == "" {
			u.mode = ""
			return u.dispatch(a)
		}
		u.inputError = "Add this task or cancel to leave."
		return nil
	}
	if isKey(k, tcell.KeyEnter) || a.kind == "input_save" || u.mode == "search" && isKey(k, tcell.KeyDown) {
		if u.mode == "search" {
			u.mode = ""
			u.selectTask(0)
			return nil
		}
		state, view := "planned", "active"
		if u.mode == "record" {
			state, view = "done", "today"
		}
		task, err := u.store.Add(u.input.String(), u.context, state, "", u.inputSpace)
		if err != nil {
			u.inputError = err.Error()
			return nil
		}
		u.mode, u.search = "", ""
		u.switchView(view, u.allProjects)
		u.position.id = task.ID
		u.notify("Added to " + u.context.Label)
		return nil
	}
	if k != nil {
		u.input.handle(k)
		u.inputError = ""
		if u.mode == "search" {
			u.search = u.input.String()
			u.position = position{}
		}
	}
	return nil
}

func (u *UI) editTask(task Task, focus string) {
	u.editor = &editorState{task, newInput(task.Title, 500), newInput(task.Note, 2000), task.State, focus, "", task.Space}
}

func (u *UI) handleEditor(k *tcell.EventKey, a action) error {
	e := u.editor
	if isKey(k, tcell.KeyEscape, tcell.KeyCtrlC) || a.kind == "cancel" || isKey(k, tcell.KeyEnter) && e.focus == "cancel" {
		u.editor = nil
		return nil
	}
	switch {
	case a.kind == "text":
		e.focus = a.value
		_, _, _, bw := u.panelBounds(15, 76)
		field := e.title
		if e.focus == "note" {
			field = e.note
		}
		field.click(a.cell, bw-6)
	case a.kind == "status":
		e.state, e.focus = a.value, a.value
	case a.kind == "space" || e.focus == "space" && (isKey(k, tcell.KeyEnter) || isRune(k, ' ')):
		u.chooseSpace(e.space, func(space *Space) error { e.space, e.focus = space, "save"; return nil })
	case isKey(k, tcell.KeyTab, tcell.KeyBacktab):
		step := 1
		if k.Key() == tcell.KeyBacktab {
			step = -1
		}
		e.focus = cycle(editorOrder, e.focus, step)
	case isKey(k, tcell.KeyEnter) || a.kind == "save":
		title, note := e.title.String(), e.note.String()
		after, err := u.store.Edit(e.task.ID, Edit{Title: &title, Note: &note, State: &e.state, ExpectedUpdatedAt: &e.task.UpdatedAt, SetSpace: true, Space: e.space})
		if err != nil {
			e.error = err.Error()
			return nil
		}
		if after.UpdatedAt != e.task.UpdatedAt {
			u.undo = nil
			if after.State != e.task.State {
				u.undo = &undoChange{e.task, after}
			}
		}
		u.notify("Saved: " + after.Title)
		u.editor = nil
	case e.focus == "title" && k != nil:
		e.title.handle(k)
	case e.focus == "note" && k != nil:
		e.note.handle(k)
	case validState(e.focus) && isRune(k, ' '):
		e.state = e.focus
	case validState(e.focus) && isKey(k, tcell.KeyLeft, tcell.KeyRight):
		step := 1
		if k.Key() == tcell.KeyLeft {
			step = -1
		}
		e.state = cycle([]string{"planned", "doing", "done"}, e.focus, step)
		e.focus = e.state
	}
	return nil
}

func (u *UI) showMenu(title string, options []menuOption, choose func(any) error) {
	u.menu = &menuState{title: title, options: options, choose: choose}
}

func (u *UI) handleMenu(k *tcell.EventKey, a action) error {
	m := u.menu
	if isKey(k, tcell.KeyEscape, tcell.KeyCtrlC) || !m.searchable && isRune(k, 'q') || a.kind == "back" {
		u.menu = nil
		return nil
	}
	if a.kind == "choose" || isKey(k, tcell.KeyEnter) {
		index := m.selected
		if a.kind == "choose" {
			index = a.index
		}
		if index >= 0 && index < len(m.shown) {
			u.menu = nil
			return m.choose(m.shown[index].value)
		}
		return nil
	}
	switch {
	case isKey(k, tcell.KeyDown, tcell.KeyTab) || !m.searchable && isRune(k, 'j') || a.kind == "down":
		m.selected = (m.selected + 1) % max(1, len(m.shown))
	case isKey(k, tcell.KeyUp, tcell.KeyBacktab) || !m.searchable && isRune(k, 'k') || a.kind == "up":
		m.selected = (m.selected - 1 + max(1, len(m.shown))) % max(1, len(m.shown))
	case m.searchable && isKey(k, tcell.KeyBackspace, tcell.KeyBackspace2):
		runes := []rune(m.query)
		if len(runes) > 0 {
			m.query = string(runes[:len(runes)-1])
		}
		m.selected = 0
	case m.searchable && isKey(k, tcell.KeyCtrlU):
		m.query, m.selected = "", 0
	case m.searchable && k != nil && k.Key() == tcell.KeyRune:
		m.query += string(k.Rune())
		m.selected = 0
	}
	return nil
}

func (u *UI) chooseSpace(current *Space, choose func(*Space) error) {
	spaces, err := u.spaces.Refresh()
	subtitle := ""
	if err != nil {
		spaces = nil
		subtitle = err.Error()
	}
	sort.SliceStable(spaces, func(i, j int) bool {
		a, b := spaces[i], spaces[j]
		if (a.WorkspaceID == u.context.Workspace) != (b.WorkspaceID == u.context.Workspace) {
			return a.WorkspaceID == u.context.Workspace
		}
		return strings.ToLower(a.Label) < strings.ToLower(b.Label)
	})
	options := []menuOption{{"No space · optional", (*Space)(nil)}}
	initial, target := 0, u.context.Workspace
	if current != nil {
		target = ""
		if current.Session == u.context.Session {
			target = current.WorkspaceID
		}
	}
	found := false
	for _, space := range spaces {
		suffix := ""
		if space.WorkspaceID == u.context.Workspace {
			suffix = " · here"
		}
		duplicates := 0
		for _, other := range spaces {
			if other.Label == space.Label {
				duplicates++
			}
		}
		if duplicates > 1 {
			suffix += " · " + space.WorkspaceID
		}
		if space.WorkspaceID == target {
			initial, found = len(options), true
		}
		options = append(options, menuOption{space.Label + suffix, &space})
	}
	if current != nil && !found {
		options = append(options[:1], append([]menuOption{{"Keep " + current.Label + " · unavailable here", current}}, options[1:]...)...)
		initial = 1
	}
	u.menu = &menuState{title: "Attach a space", subtitle: subtitle, options: options, selected: initial, searchable: true,
		choose: func(value any) error { return choose(value.(*Space)) }}
}

func (u *UI) handlePage(k *tcell.EventKey, a action) {
	if isKey(k, tcell.KeyEscape, tcell.KeyCtrlC, tcell.KeyEnter) || isRune(k, 'q') || a.kind == "back" {
		u.page = nil
		return
	}
	_, h := u.screen.Size()
	switch {
	case isKey(k, tcell.KeyDown) || isRune(k, 'j') || a.kind == "down":
		u.page.offset++
	case isKey(k, tcell.KeyUp) || isRune(k, 'k') || a.kind == "up":
		u.page.offset--
	case isKey(k, tcell.KeyPgDn):
		u.page.offset += max(1, h-4)
	case isKey(k, tcell.KeyPgUp):
		u.page.offset -= max(1, h-4)
	}
}

func (u *UI) changeState(task Task, state string) error {
	before, err := u.store.Get(task.ID)
	if err != nil {
		return err
	}
	after, err := u.store.Edit(task.ID, Edit{State: &state, ExpectedUpdatedAt: &before.UpdatedAt})
	if err != nil {
		return err
	}
	u.undo = nil
	if before.State != after.State {
		u.undo = &undoChange{before, after}
	}
	u.notify(map[string]string{"done": "Completed", "doing": "Started", "planned": "Moved to up next"}[state] + ": " + after.Title)
	u.position.id = task.ID
	return nil
}

func (u *UI) dispatch(a action) error {
	index := u.position.index
	if a.index >= 0 && (a.kind == "task" || a.kind == "select" || a.kind == "toggle" || a.kind == "go") {
		index = a.index
	}
	var task *Task
	if len(u.tasks) > 0 {
		task = &u.tasks[min(max(index, 0), len(u.tasks)-1)]
	}
	switch a.kind {
	case "close":
		if u.search != "" {
			u.search, u.position.offset = "", 0
		} else {
			u.done = true
		}
	case "view":
		u.switchView(a.value, u.allProjects)
	case "scope":
		u.showMenu("Show tasks from", []menuOption{{u.context.Label + " + tasks attached here", false}, {"All projects on this machine", true}}, func(value any) error { u.switchView(u.view, value.(bool)); return nil })
	case "up":
		u.selectTask(u.position.index - 1)
	case "down":
		u.selectTask(u.position.index + 1)
	case "clear":
		u.search, u.position.offset = "", 0
	case "search", "add", "record":
		u.beginInput(a.kind)
	case "toggle":
		if task != nil {
			state := "done"
			if task.State == "done" {
				state = "planned"
			}
			return u.changeState(*task, state)
		}
	case "start":
		if task != nil {
			state := "doing"
			if task.State == "doing" {
				state = "planned"
			}
			return u.changeState(*task, state)
		}
	case "link":
		if task == nil {
			return nil
		}
		before, err := u.store.Get(task.ID)
		if err != nil {
			return err
		}
		u.chooseSpace(before.Space, func(space *Space) error {
			after, err := u.store.Edit(before.ID, Edit{SetSpace: true, Space: space, ExpectedUpdatedAt: &before.UpdatedAt})
			if err != nil {
				return err
			}
			if after.UpdatedAt != before.UpdatedAt {
				u.undo = nil
			}
			u.notify("Space: " + u.spaces.Label(space))
			return nil
		})
	case "go":
		if task != nil {
			if task.Space == nil {
				return u.dispatch(action{kind: "link", index: -1})
			}
			if err := u.spaces.Focus(task.Space); err != nil {
				return err
			}
			u.done = true
		}
	case "undo":
		if u.undo == nil {
			return nil
		}
		change := u.undo
		u.undo = nil
		if _, err := u.store.UndoState(change.before, change.after); err != nil {
			return err
		}
		view := "active"
		if change.before.State == "done" {
			view = "history"
		}
		u.switchView(view, u.allProjects)
		u.position.id = change.before.ID
		u.notify("Undone: " + change.before.Title)
	case "select", "task", "edit", "note":
		if task == nil {
			return nil
		}
		u.selectTask(index)
		if a.kind == "select" {
			previous, at := u.lastClickID, u.lastClickAt
			u.lastClickID, u.lastClickAt = task.ID, time.Now()
			if previous != task.ID || time.Since(at) >= 400*time.Millisecond {
				return nil
			}
			u.lastClickID = 0
		}
		fresh, err := u.store.Get(task.ID)
		if err != nil {
			return err
		}
		focus := "title"
		if a.kind == "note" {
			focus = "note"
		}
		u.editTask(fresh, focus)
	case "details":
		if task == nil {
			return nil
		}
		events, err := u.store.Events(&task.ID)
		if err != nil {
			return err
		}
		lines := []string{"Status: " + task.State, "Project: " + task.ProjectLabel, "Space: " + u.spaces.Label(task.Space), "", first(task.Note, "No note yet."), "", "Activity"}
		for _, e := range events {
			lines = append(lines, localTime(e.At)+"  "+e.Kind+"  "+e.Title)
		}
		u.page = &pageState{title: task.Title, lines: lines}
	case "more":
		options := []menuOption{}
		if u.undo != nil {
			options = append(options, menuOption{"Undo last status change", "undo"})
		}
		if task != nil {
			if task.Space != nil {
				options = append(options, menuOption{"Open attached space", "go"})
			}
			options = append(options, menuOption{"Attach or change space", "link"}, menuOption{"Selected task activity", "details"})
		}
		options = append(options, menuOption{"Record work already finished", "record"}, menuOption{"Choose project view", "scope"}, menuOption{"Help & shortcuts", "help"}, menuOption{"Storage information", "info"})
		u.showMenu("More actions", options, func(value any) error { return u.dispatch(action{kind: value.(string), index: -1}) })
	case "help":
		u.page = &pageState{title: "Getting around", lines: helpLines}
	case "info":
		u.page = &pageState{title: "Storage", lines: []string{"Database", u.store.path, "", "Current project", u.context.Project, "", "Tasks are stored locally on this machine and survive closing a workspace.", "", "Export from a checkout:", "./bin/herdr-todo --db <database above> export > tasks.json", "", "Herdr Todo " + version()}}
	}
	return nil
}

var helpLines = []string{
	"New adds a task inline. Find filters the list as you type.",
	"Click once to select; double-click or Enter to edit. The editor saves title, note, status and space together.",
	"Click a checkbox to complete a task. Undo restores its previous status.",
	"Spaces are optional: Tab while adding or l in the list opens a searchable picker. Click the space under a task or press g to open it.",
	"Attached tasks also appear when you open Todo in that space. They stay in their original project too. Choose No space to remove the link.",
	"Tabs remember your position for this popup. Wide windows show a preview beside the list.", "", "Keyboard shortcuts",
	"a / A   New task / Record finished work", "Enter / e   Edit selected task", "n   Edit note", "Space   Complete / Reopen", "s   Start / Move to up next", "l / g   Attach space / Open space", "u   Undo", "/   Live search", "Tab, Left/Right, 1-3   Tasks / Today / History", "w   Project view", "d   Selected task activity", "?   Help", "Esc   Cancel input, clear search, then close", "",
	"In the editor: Tab changes fields; arrows or Space choose a focused status. Space or Enter opens the space picker. Save commits the whole draft; Escape cancels it.",
	"Today uses the local date. Reopened tasks keep earlier completions in their activity.",
}

func localTime(stamp string) string {
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return ""
	}
	return t.In(time.Local).Format("2006-01-02 15:04")
}

func runTUI(store *Store, c Context) error {
	screen, err := tcell.NewScreen()
	if err != nil {
		return err
	}
	if err := screen.Init(); err != nil {
		return err
	}
	defer screen.Fini()
	screen.EnableMouse(tcell.MouseButtonEvents)
	screen.EnablePaste()
	u := newUI(screen, store, c, nil)
	if err := u.refresh(); err != nil {
		return err
	}
	for _, task := range u.tasks {
		if task.Space != nil {
			_, _ = u.spaces.Refresh()
			break
		}
	}
	stop, signals := make(chan struct{}), make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	defer close(stop)
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = screen.PostEvent(tcell.NewEventInterrupt(nil))
			case <-signals:
				_ = screen.PostEvent(tcell.NewEventInterrupt("quit"))
			}
		}
	}()
	for !u.done {
		if err := u.refresh(); err != nil {
			return fmt.Errorf("load tasks: %w", err)
		}
		u.draw()
		event := screen.PollEvent()
		if event == nil {
			break
		}
		if err := u.handle(event); err != nil {
			u.notify(err.Error())
		}
	}
	return nil
}
