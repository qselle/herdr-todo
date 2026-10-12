package main

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"
)

var normal = tcell.StyleDefault
var dim = normal.Dim(true)
var bold = normal.Bold(true)
var accent = normal.Foreground(tcell.ColorTeal).Bold(true)
var reverse = normal.Reverse(true)

func stateStyle(state string) tcell.Style {
	switch state {
	case "doing":
		return normal.Foreground(tcell.ColorYellow)
	case "done":
		return normal.Foreground(tcell.ColorGreen)
	}
	return normal
}

func (u *UI) put(y, x int, text string, style tcell.Style) {
	w, h := u.screen.Size()
	if y < 0 || y >= h || x < 0 || x >= w {
		return
	}
	g := uniseg.NewGraphemes(clean(text))
	for g.Next() {
		runes, width := g.Runes(), g.Width()
		if x+width > w {
			break
		}
		if len(runes) > 0 {
			u.screen.SetContent(x, y, runes[0], runes[1:], style)
		}
		x += width
	}
}

func (u *UI) hit(y, x, width int, a action) {
	w, h := u.screen.Size()
	if y >= 0 && y < h && x < w {
		u.hits = append(u.hits, hit{y, max(0, x), min(w, x+width), a})
	}
}

func (u *UI) button(y, x int, label string, a action, active, focused bool) int {
	text, style := " "+label+" ", accent
	if active {
		style = reverse
	}
	if focused {
		style = style.Underline(true)
	}
	u.put(y, x, text, style)
	u.hit(y, x, cellWidth(text), a)
	return x + cellWidth(text) + 1
}

func cmd(kind string) action                 { return action{kind: kind, index: -1} }
func cmdValue(kind, value string) action     { return action{kind: kind, value: value, index: -1} }
func cmdIndex(kind string, index int) action { return action{kind: kind, index: index} }

func (u *UI) draw() {
	u.screen.Clear()
	u.screen.HideCursor()
	u.hits = nil
	w, h := u.screen.Size()
	if w < 36 || h < 12 {
		u.put(0, 0, "More room needed (36 x 12).", normal)
		u.put(1, 0, "Resize to continue. Esc closes.", normal)
		u.screen.Show()
		return
	}
	if u.page != nil {
		u.drawPage()
		u.screen.Show()
		return
	}
	u.drawList()
	if u.editor != nil {
		u.drawEditor()
	}
	if u.menu != nil {
		u.drawMenu()
	}
	u.screen.Show()
}

func (u *UI) drawList() {
	w, h := u.screen.Size()
	scope := u.context.Label
	if u.allProjects {
		scope = "All projects"
	}
	u.button(0, 1, shorten(scope, w-17)+" ▾", cmd("scope"), false, false)
	u.button(0, w-12, "Esc Close", cmd("close"), false, false)
	x := 1
	for _, view := range views {
		label := viewLabels[view]
		if view != "history" {
			count := fmt.Sprint(u.counts[view])
			if w < 44 && u.counts[view] > 99 {
				count = "99+"
			}
			label += " " + count
		}
		x = u.button(1, x, label, cmdValue("view", view), view == u.view, false)
	}
	x = u.button(2, 1, "+ New [a]", cmd("add"), false, false)
	x = u.button(2, x, "Find [/]", cmd("search"), false, false)
	u.button(2, x, "More [m]", cmd("more"), false, false)
	top, bottom := 4, h-2
	if u.mode != "" {
		label := map[string]string{"add": "+ ", "record": "✓ ", "search": "/ "}[u.mode]
		u.put(3, 1, label, accent)
		available := w - 6
		text, cursor, _ := u.input.viewport(available)
		u.put(3, 3, strings.Repeat(" ", available), reverse)
		u.put(3, 3, text, reverse)
		if text == "" {
			placeholder := map[string]string{"add": "What needs doing?", "record": "What did you finish?", "search": "Find a task…"}[u.mode]
			u.put(3, 3, shorten(placeholder, available), reverse.Dim(true))
		}
		u.hit(3, 3, available, cmdValue("text", "inline"))
		u.screen.ShowCursor(3+cursor, 3)
		top = 5
	} else if u.search != "" {
		u.put(3, 2, shorten("Matching: "+u.search, w-14), dim)
		u.button(3, w-11, "× Clear", cmd("clear"), false, false)
		top = 5
	}
	detailX, listWidth := 0, w
	if w >= 94 && h >= 18 {
		detailX = max(50, w*3/5)
		listWidth = detailX - 1
	}
	if len(u.tasks) == 0 {
		title := map[string]string{"active": "A little room for your next idea.", "today": "A fresh start today.", "history": "Your finished work lives here."}[u.view]
		hint := map[string]string{"active": "Add a task when you're ready.", "today": "Finished something? Record it below.", "history": "Complete a task to start your history."}[u.view]
		if u.search != "" {
			title, hint = "No matching tasks.", "Try another word or clear the search."
		}
		y := top
		if h >= 18 {
			y++
		}
		for _, line := range wrapCells(title, listWidth-4) {
			if y < bottom {
				u.put(y, 2, line, bold)
				y++
			}
		}
		if y+1 < bottom {
			u.put(y+1, 2, shorten(hint, listWidth-4), dim)
		}
		if y+3 < bottom && u.mode == "" {
			label := map[string]string{"clear": "Clear search", "record": "+ Record finished work", "add": "+ Add a task"}[u.emptyAction()]
			u.button(y+3, 2, label, cmd(u.emptyAction()), true, false)
		}
	} else {
		u.drawTaskRows(top, bottom, listWidth, detailX != 0)
		if detailX != 0 {
			u.drawPreview(u.tasks[u.position.index], top, bottom, detailX, w-detailX-3)
		}
	}
	if u.mode != "" {
		label := "↵ Add"
		if u.mode == "search" {
			label = "↵ Done"
		}
		x = u.button(h-2, 1, label, cmd("input_save"), false, false)
		if u.mode != "search" {
			x = u.button(h-2, x, "Tab Space", cmd("input_space"), false, false)
		}
		u.button(h-2, x, "Esc Cancel", cmd("input_cancel"), false, false)
		hint := "↓ to results · live search"
		if u.mode != "search" {
			hint = "Space: " + u.spaces.Label(u.inputSpace) + " (optional)"
		}
		style := dim
		if u.inputError != "" {
			hint, style = u.inputError, accent
		}
		u.put(h-1, 2, shorten(hint, w-4), style)
		return
	}
	message := "Your plans and progress, in one place."
	if len(u.tasks) > 0 {
		task := u.tasks[u.position.index]
		x = u.button(h-2, 1, "Enter Edit", cmdIndex("task", u.position.index), false, false)
		label := "Space Done"
		if task.State == "done" {
			label = "Space Reopen"
		}
		u.button(h-2, x, label, cmdIndex("toggle", u.position.index), false, false)
		if w >= 44 {
			end := u.button(h-2, x+16, "l Space", cmd("link"), false, false)
			if w >= 64 {
				label = "s Start"
				if task.State == "doing" {
					label = "s Plan"
				}
				end = u.button(h-2, end, label, cmd("start"), false, false)
			}
			if w >= 78 && task.Space != nil {
				u.button(h-2, end, "g Open space", cmd("go"), false, false)
			}
		}
		hint := "l Link space"
		if task.Space != nil {
			hint = "g Open space"
		}
		message = fmt.Sprintf("%d / %d · ↑↓ · %s", u.position.index+1, len(u.tasks), hint)
	} else {
		label := map[string]string{"clear": "Clear search", "record": "Record finished work", "add": "+ New task"}[u.emptyAction()]
		u.button(h-2, 1, label, cmd(u.emptyAction()), false, false)
	}
	if time.Now().Before(u.messageUntil) {
		message = u.message
	}
	available := w - 4
	if u.undo != nil {
		available = w - 16
	}
	u.put(h-1, 2, shorten(message, available), dim)
	if u.undo != nil {
		u.button(h-1, w-12, "u Undo", cmd("undo"), false, false)
	}
}

type taskLine struct {
	kind  string
	index int
	text  string
}

func (u *UI) drawTaskRows(top, bottom, width int, preview bool) {
	_, h := u.screen.Size()
	lines := []taskLine{}
	starts, ends := make([]int, len(u.tasks)), make([]int, len(u.tasks))
	group := ""
	for i, task := range u.tasks {
		section := "UP NEXT"
		if u.view == "active" {
			if task.State == "doing" {
				section = "IN PROGRESS"
			}
		} else {
			section = "Completed"
			if task.CompletedAt != nil {
				stamp := localTime(*task.CompletedAt)
				if len(stamp) >= 10 {
					section = stamp[:10]
				}
			}
		}
		if section != group {
			lines = append(lines, taskLine{"heading", -1, section})
			group = section
		}
		starts[i] = len(lines)
		titles := wrapCells(task.Title, width-9)
		maxLines := 2
		if i == u.position.index {
			maxLines = 3
		}
		if len(titles) > maxLines {
			titles = titles[:maxLines]
			titles[maxLines-1] = shorten(titles[maxLines-1]+" …", width-9)
		}
		for j, line := range titles {
			kind := "continuation"
			if j == 0 {
				kind = "task"
			}
			lines = append(lines, taskLine{kind, i, line})
		}
		if task.Space != nil {
			lines = append(lines, taskLine{"space", i, shorten("↗ "+u.spaces.Label(task.Space), width-9)})
		}
		meta := []string{}
		if u.allProjects {
			meta = append(meta, task.ProjectLabel)
		}
		if task.CompletedAt != nil {
			stamp := localTime(*task.CompletedAt)
			if len(stamp) >= 16 {
				meta = append(meta, stamp[11:])
			}
		}
		if task.Note != "" && i == u.position.index && !preview {
			meta = append(meta, task.Note)
		}
		if len(meta) > 0 && h >= 18 {
			lines = append(lines, taskLine{"meta", i, shorten(strings.Join(meta, " · "), width-9)})
		}
		ends[i] = len(lines)
		if h >= 26 {
			lines = append(lines, taskLine{"gap", i, ""})
		}
	}
	visible := max(1, bottom-top)
	start, end := starts[u.position.index], ends[u.position.index]
	u.position.offset = min(u.position.offset, start)
	if end > u.position.offset+visible {
		u.position.offset = min(start, max(0, end-visible))
	}
	u.position.offset = min(u.position.offset, max(0, len(lines)-visible))
	for n := u.position.offset; n < min(len(lines), u.position.offset+visible); n++ {
		row, line := top+n-u.position.offset, lines[n]
		if line.kind == "heading" {
			u.put(row, 2, line.text, dim)
			continue
		}
		if line.kind == "gap" {
			continue
		}
		task := u.tasks[line.index]
		style := stateStyle(task.State)
		if line.index == u.position.index {
			style = reverse
		}
		u.put(row, 1, strings.Repeat(" ", max(0, width-3)), style)
		textStyle := style
		if line.kind == "meta" {
			textStyle = style.Dim(true)
		}
		u.put(row, 7, line.text, textStyle)
		u.hit(row, 1, width-3, cmdIndex("select", line.index))
		if line.kind == "space" {
			u.hit(row, 7, cellWidth(line.text), cmdIndex("go", line.index))
		}
		if line.kind == "task" {
			u.put(row, 2, stateIcons[task.State], style)
			u.hit(row, 2, 3, cmdIndex("toggle", line.index))
		}
	}
	if len(lines) > visible {
		thumb := top + int(math.Round(float64(u.position.offset)/float64(max(1, len(lines)-visible))*float64(visible-1)))
		for row := top; row < bottom; row++ {
			char := "│"
			if row == thumb {
				char = "┃"
			}
			u.put(row, width-2, char, dim)
		}
	}
}

func (u *UI) drawPreview(task Task, top, bottom, x, available int) {
	for row := top; row < bottom; row++ {
		u.put(row, x, "│", dim)
	}
	x += 2
	y := top
	titleLines := wrapCells(task.Title, available)
	for _, line := range titleLines[:min(3, len(titleLines))] {
		u.put(y, x, line, bold)
		y++
	}
	status := map[string]string{"planned": "Up next", "doing": "In progress", "done": "Completed"}[task.State]
	u.put(y+1, x, status, stateStyle(task.State))
	u.put(y+2, x, shorten(task.ProjectLabel, available), dim)
	y += 4
	if task.Space != nil && y < bottom-1 {
		u.button(y, x, shorten("↗ "+u.spaces.Label(task.Space), available-2), cmd("go"), false, false)
		y += 2
	}
	for _, line := range wrapCells(first(task.Note, "Add a note when you open this task."), available) {
		if y >= bottom-4 {
			break
		}
		style := normal
		if task.Note == "" {
			style = dim
		}
		u.put(y, x, line, style)
		y++
	}
	if y+2 < bottom {
		label := "Added " + localTime(task.CreatedAt)
		if task.CompletedAt != nil {
			label = "Completed " + localTime(*task.CompletedAt)
		}
		u.put(bottom-3, x, shorten(label, available), dim)
		u.button(bottom-1, x, "Open task ↵", cmdIndex("task", u.position.index), false, false)
	}
}

func (u *UI) panelBounds(height, maxWidth int) (int, int, int, int) {
	w, h := u.screen.Size()
	bw, bh := min(maxWidth, w-2), min(height, h-2)
	return (h - bh) / 2, (w - bw) / 2, bh, bw
}

func (u *UI) panel(title string, height, maxWidth int) (int, int, int, int) {
	u.hits = nil
	u.screen.HideCursor()
	y, x, bh, bw := u.panelBounds(height, maxWidth)
	for row := y; row < y+bh; row++ {
		u.put(row, x, strings.Repeat(" ", bw), normal)
	}
	u.put(y, x, "┌"+strings.Repeat("─", bw-2)+"┐", accent)
	u.put(y+bh-1, x, "└"+strings.Repeat("─", bw-2)+"┘", accent)
	for row := y + 1; row < y+bh-1; row++ {
		u.put(row, x, "│", normal)
		u.put(row, x+bw-1, "│", normal)
	}
	u.put(y, x+2, " "+shorten(title, bw-6)+" ", bold)
	return y, x, bh, bw
}

func (u *UI) drawMenu() {
	m := u.menu
	y, x, bh, bw := u.panel(m.title, len(m.options)+6, 66)
	m.shown = nil
	for _, option := range m.options {
		if containsFold(option.label, m.query) {
			m.shown = append(m.shown, option)
		}
	}
	m.selected = min(m.selected, max(0, len(m.shown)-1))
	hint := "↑ ↓ choose · Enter select"
	if m.searchable {
		hint = "Type to find · ↑↓ Enter"
	}
	if m.subtitle != "" {
		hint = m.subtitle
	}
	if m.query != "" {
		hint = "Find: " + m.query
	}
	u.put(y+1, x+2, shorten(hint, bw-4), dim)
	available := bh - 4
	start := max(0, m.selected-available+1)
	for i := start; i < min(len(m.shown), start+available); i++ {
		u.button(y+2+i-start, x+2, shorten(m.shown[i].label, bw-6), cmdIndex("choose", i), i == m.selected, false)
	}
	if len(m.shown) == 0 {
		u.put(y+2, x+3, "No matching spaces", dim)
	}
	end := u.button(y+bh-2, x+2, "Esc Back", cmd("back"), false, false)
	u.put(y+bh-2, end, shorten("↑↓ Enter select", x+bw-1-end), dim)
}

func (u *UI) drawEditor() {
	e := u.editor
	y, x, bh, bw := u.panel("Edit task", 15, 76)
	compact := bh < 13
	titleY, noteY, stateY, spaceY := y+2, y+5, y+8, y+10
	if compact {
		noteY, stateY, spaceY = y+4, y+5, y+6
	}
	available := bw - 6
	for _, field := range []struct {
		name, label string
		row         int
		input       *TextInput
	}{{"title", "Title", titleY, e.title}, {"note", "Note (optional)", noteY, e.note}} {
		u.put(field.row-1, x+3, field.label, dim)
		text, cursor, _ := field.input.viewport(available)
		style := normal.Underline(true)
		if e.focus == field.name {
			style = reverse
		}
		u.put(field.row, x+3, strings.Repeat(" ", available), style)
		u.put(field.row, x+3, text, style)
		u.hit(field.row, x+3, available, cmdValue("text", field.name))
		if e.focus == field.name {
			u.screen.ShowCursor(x+3+cursor, field.row)
		}
	}
	statusX := x + 2
	for _, state := range []struct{ value, label string }{{"planned", "Planned"}, {"doing", "Doing"}, {"done", "Done"}} {
		statusX = u.button(stateY, statusX, state.label, cmdValue("status", state.value), e.state == state.value, e.focus == state.value)
	}
	u.button(spaceY, x+2, shorten("Space: "+u.spaces.Label(e.space)+" ▾", bw-6), cmd("space"), e.focus == "space", false)
	hint := "Tab fields · Enter saves"
	if validState(e.focus) {
		hint = "Space picks · Enter saves"
	}
	if e.focus == "space" {
		hint = "Space or Enter to choose"
	}
	style := dim
	if e.error != "" {
		hint, style = e.error, accent
	}
	hintY := y + bh - 4
	if compact {
		hintY = spaceY + 1
	}
	u.put(hintY, x+3, shorten(hint, bw-6), style)
	end := u.button(y+bh-2, x+2, "Enter Save", cmd("save"), e.focus == "save", false)
	u.button(y+bh-2, end, "Esc Cancel", cmd("cancel"), e.focus == "cancel", false)
}

func (u *UI) drawPage() {
	p := u.page
	w, h := u.screen.Size()
	u.put(0, 2, shorten(p.title, w-4), bold)
	wrapped := []string{}
	for _, line := range p.lines {
		wrapped = append(wrapped, wrapCells(line, max(1, w-5))...)
	}
	visible := max(1, h-4)
	p.offset = max(0, min(p.offset, len(wrapped)-visible))
	for i := p.offset; i < min(len(wrapped), p.offset+visible); i++ {
		u.put(2+i-p.offset, 2, wrapped[i], normal)
	}
	u.button(h-1, 1, "Esc Back", cmd("back"), false, false)
	u.put(h-1, 14, "↑ ↓ scroll", dim)
}
