package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

type fakeSpaces struct {
	items []Space
	focus *Space
	err   error
}

func (s *fakeSpaces) Refresh() ([]Space, error) { return s.items, s.err }
func (s *fakeSpaces) Label(link *Space) string {
	if link == nil {
		return "None"
	}
	for _, item := range s.items {
		if item.WorkspaceID == link.WorkspaceID && item.Session == link.Session {
			return item.Label
		}
	}
	return link.Label
}
func (s *fakeSpaces) Focus(link *Space) error {
	if s.err == nil {
		s.focus = link
	}
	return s.err
}

func testUI(t *testing.T, w, h int) (*UI, *fakeSpaces) {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	screen.SetSize(w, h)
	t.Cleanup(screen.Fini)
	spaces := &fakeSpaces{items: []Space{{"w1", "Project", testContext.Session}, {"w2", "Release", testContext.Session}, {"w3", "日本語 Lab", testContext.Session}}}
	u := newUI(screen, testStore(t), testContext, spaces)
	drawUI(t, u)
	return u, spaces
}

func drawUI(t *testing.T, u *UI) {
	t.Helper()
	if err := u.refresh(); err != nil {
		t.Fatal(err)
	}
	u.draw()
}

func sendEvent(t *testing.T, u *UI, event tcell.Event) {
	t.Helper()
	if err := u.handle(event); err != nil {
		t.Fatal(err)
	}
	drawUI(t, u)
}

func key(t *testing.T, u *UI, code tcell.Key) {
	t.Helper()
	sendEvent(t, u, tcell.NewEventKey(code, 0, tcell.ModNone))
}
func typeText(t *testing.T, u *UI, text string) {
	t.Helper()
	for _, r := range text {
		sendEvent(t, u, tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
}

func clickAction(t *testing.T, u *UI, kind, value string) {
	t.Helper()
	for i := len(u.hits) - 1; i >= 0; i-- {
		h := u.hits[i]
		if h.action.kind == kind && (value == "" || h.action.value == value) {
			sendEvent(t, u, tcell.NewEventMouse(h.left, h.y, tcell.Button1, tcell.ModNone))
			sendEvent(t, u, tcell.NewEventMouse(h.left, h.y, tcell.ButtonNone, tcell.ModNone))
			return
		}
	}
	t.Fatalf("missing clickable action %s %s", kind, value)
}

func screenText(u *UI) string {
	w, h := u.screen.Size()
	var text strings.Builder
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, comb, _, _ := u.screen.GetContent(x, y)
			if r == 0 {
				r = ' '
			}
			text.WriteRune(r)
			text.WriteString(string(comb))
		}
		text.WriteByte('\n')
	}
	return text.String()
}

func TestInlineCaptureSpaceAndJump(t *testing.T) {
	u, spaces := testUI(t, 80, 24)
	typeText(t, u, "aCross-space checklist")
	key(t, u, tcell.KeyTab)
	if u.menu == nil || u.menu.shown[u.menu.selected].value.(*Space).WorkspaceID != "w1" {
		t.Fatal("current space should be the initial choice")
	}
	typeText(t, u, "Release")
	key(t, u, tcell.KeyEnter)
	if u.mode != "add" || u.inputSpace == nil {
		t.Fatal("picker lost capture draft")
	}
	key(t, u, tcell.KeyEnter)
	if len(u.tasks) != 1 || u.tasks[0].Space.WorkspaceID != "w2" {
		t.Fatalf("capture: %+v", u.tasks)
	}
	task := u.tasks[0]
	clickAction(t, u, "toggle", "")
	if len(u.tasks) != 0 || u.undo == nil {
		t.Fatal("checkbox did not complete task")
	}
	typeText(t, u, "u")
	if len(u.tasks) != 1 || !sameSpace(u.tasks[0].Space, task.Space) {
		t.Fatal("undo lost space attachment")
	}
	clickAction(t, u, "go", "")
	if !u.done || spaces.focus == nil || spaces.focus.WorkspaceID != "w2" {
		t.Fatal("space link did not focus destination")
	}
	// The originating project stays unchanged; the destination also shows it.
	u.context.Project, u.context.Workspace = "/work/release", "w2"
	drawUI(t, u)
	if len(u.tasks) != 1 || u.tasks[0].Project != testContext.Project {
		t.Fatal("attached task missing in destination")
	}
}

func TestEditorAtomicCancelSaveAndFieldTraversal(t *testing.T) {
	u, _ := testUI(t, 46, 17)
	task := addTask(t, u.store, "Original", "planned")
	drawUI(t, u)
	key(t, u, tcell.KeyEnter)
	key(t, u, tcell.KeyCtrlU)
	typeText(t, u, "Revised")
	key(t, u, tcell.KeyTab)
	typeText(t, u, "Draft note")
	key(t, u, tcell.KeyTab)
	key(t, u, tcell.KeyTab)
	if u.editor.focus != "doing" || u.editor.state != "planned" {
		t.Fatal("Tab should move focus without changing status")
	}
	typeText(t, u, " ")
	clickAction(t, u, "space", "")
	typeText(t, u, "Release")
	key(t, u, tcell.KeyEnter)
	if u.editor.focus != "save" {
		t.Fatal("picker must return to Save")
	}
	key(t, u, tcell.KeyEscape)
	original, _ := u.store.Get(task.ID)
	if original.Title != "Original" || original.Note != "" || original.State != "planned" || original.Space != nil {
		t.Fatalf("cancel committed draft: %+v", original)
	}
	key(t, u, tcell.KeyEnter)
	key(t, u, tcell.KeyCtrlU)
	typeText(t, u, "Revised")
	clickAction(t, u, "status", "done")
	clickAction(t, u, "space", "")
	typeText(t, u, "Release")
	key(t, u, tcell.KeyEnter)
	key(t, u, tcell.KeyEnter)
	after, _ := u.store.Get(task.ID)
	if after.Title != "Revised" || after.State != "done" || after.Space == nil {
		t.Fatalf("editor save: %+v", after)
	}
	if u.menu != nil || u.editor != nil {
		t.Fatal("Enter reopened picker instead of saving")
	}
}

func TestEditorRejectsConcurrentChange(t *testing.T) {
	u, _ := testUI(t, 80, 24)
	task := addTask(t, u.store, "Original", "planned")
	drawUI(t, u)
	key(t, u, tcell.KeyEnter)
	typeText(t, u, " draft")
	_, err := u.store.Edit(task.ID, Edit{Note: ptr("Changed elsewhere")})
	if err != nil {
		t.Fatal(err)
	}
	key(t, u, tcell.KeyEnter)
	if u.editor == nil || !strings.Contains(u.editor.error, "changed elsewhere") {
		t.Fatal("stale editor was accepted")
	}
	after, _ := u.store.Get(task.ID)
	if after.Title != "Original" || after.Note != "Changed elsewhere" {
		t.Fatal("stale draft overwrote external edit")
	}
}

func TestSearchCheckboxAndCancelSelection(t *testing.T) {
	u, _ := testUI(t, 80, 24)
	addTask(t, u.store, "First", "planned")
	second := addTask(t, u.store, "Second", "planned")
	drawUI(t, u)
	typeText(t, u, "j")
	selected := u.position.id
	typeText(t, u, "/Sec")
	key(t, u, tcell.KeyEscape)
	if u.search != "" || u.position.id != selected {
		t.Fatal("cancel search lost previous position")
	}
	typeText(t, u, "/Second")
	clickAction(t, u, "toggle", "")
	after, _ := u.store.Get(second.ID)
	if after.State != "done" || u.mode != "" {
		t.Fatal("search result checkbox did not act immediately")
	}
	typeText(t, u, "u")
	if u.position.id != second.ID {
		t.Fatal("undo lost selection")
	}
}

func TestSearchSpaceUsesFreshLabel(t *testing.T) {
	u, spaces := testUI(t, 80, 24)
	_, err := u.store.Add("A task", testContext, "planned", "", &Space{"w2", "Old name", testContext.Session})
	if err != nil {
		t.Fatal(err)
	}
	spaces.items[1].Label = "Renamed lab"
	drawUI(t, u)
	typeText(t, u, "/renamed")
	if len(u.tasks) != 1 {
		t.Fatal("live label was not searchable")
	}
	clickAction(t, u, "go", "")
	if !u.done || spaces.focus == nil {
		t.Fatal("link did not work during live search")
	}
}

func TestResizeRetainsDraftAndSpacePicker(t *testing.T) {
	u, _ := testUI(t, 100, 30)
	typeText(t, u, "a界面 task")
	u.screen.(tcell.SimulationScreen).SetSize(20, 8)
	drawUI(t, u)
	u.screen.(tcell.SimulationScreen).SetSize(36, 12)
	drawUI(t, u)
	key(t, u, tcell.KeyTab)
	typeText(t, u, "日本")
	u.screen.(tcell.SimulationScreen).SetSize(20, 8)
	drawUI(t, u)
	u.screen.(tcell.SimulationScreen).SetSize(36, 12)
	drawUI(t, u)
	key(t, u, tcell.KeyEnter)
	key(t, u, tcell.KeyEnter)
	if len(u.tasks) != 1 || u.tasks[0].Title != "界面 task" || u.tasks[0].Space.WorkspaceID != "w3" {
		t.Fatal("resize lost draft or picker")
	}
	key(t, u, tcell.KeyEnter)
	key(t, u, tcell.KeyCtrlU)
	typeText(t, u, "Revised 界面")
	u.screen.(tcell.SimulationScreen).SetSize(20, 8)
	drawUI(t, u)
	u.screen.(tcell.SimulationScreen).SetSize(120, 32)
	drawUI(t, u)
	key(t, u, tcell.KeyEnter)
	if u.tasks[0].Title != "Revised 界面" {
		t.Fatal("resize lost editor draft")
	}
}

func TestUnavailableSpaceIsKeptAndRemovable(t *testing.T) {
	u, spaces := testUI(t, 80, 24)
	link := &Space{"closed", "Closed lab", "other-session"}
	_, err := u.store.Add("Keep attachment", testContext, "planned", "", link)
	if err != nil {
		t.Fatal(err)
	}
	spaces.err = errors.New("Herdr unavailable")
	drawUI(t, u)
	typeText(t, u, "l")
	key(t, u, tcell.KeyEnter)
	if !sameSpace(u.tasks[0].Space, link) {
		t.Fatal("offline picker detached a task")
	}
	typeText(t, u, "l")
	typeText(t, u, "No space")
	key(t, u, tcell.KeyEnter)
	if u.tasks[0].Space != nil {
		t.Fatal("explicit detach did not work")
	}
}

func TestInputValidationAndPasteDoesNotRunShortcuts(t *testing.T) {
	u, _ := testUI(t, 80, 24)
	typeText(t, u, "a")
	key(t, u, tcell.KeyEnter)
	if u.mode != "add" || !strings.Contains(u.inputError, "needs a title") {
		t.Fatal("empty title abandoned draft")
	}
	sendEvent(t, u, tcell.NewEventPaste(true))
	typeText(t, u, "gq 日本語")
	key(t, u, tcell.KeyEnter)
	typeText(t, u, "next")
	sendEvent(t, u, tcell.NewEventPaste(false))
	if u.input.String() != "gq 日本語 next" || u.done {
		t.Fatal("paste dispatched commands")
	}
	key(t, u, tcell.KeyEnter)
	if len(u.tasks) != 1 {
		t.Fatal("pasted task missing")
	}
	sendEvent(t, u, tcell.NewEventPaste(true))
	typeText(t, u, "qa")
	sendEvent(t, u, tcell.NewEventPaste(false))
	if u.done || u.mode != "" {
		t.Fatal("list paste ran shortcuts")
	}
}

func TestSelectionSurvivesReorderAndTabs(t *testing.T) {
	u, _ := testUI(t, 80, 24)
	for i := 0; i < 12; i++ {
		addTask(t, u.store, fmt.Sprintf("Task %d", i), "planned")
	}
	drawUI(t, u)
	typeText(t, u, "jjjjj")
	id := u.position.id
	typeText(t, u, "s")
	if u.position.id != id || u.position.index != 0 {
		t.Fatal("started task lost selection after sorting")
	}
	typeText(t, u, "2")
	typeText(t, u, "1")
	if u.position.id != id {
		t.Fatal("tab lost selection")
	}
	typeText(t, u, "ACompleted task")
	key(t, u, tcell.KeyEnter)
	if u.view != "today" || len(u.tasks) != 1 || u.tasks[0].State != "done" {
		t.Fatal("recording finished work failed")
	}
}

func TestTextInputUnicodeAndEditing(t *testing.T) {
	input := newInput("A界e\u0301 long title", 500)
	input.handle(tcell.NewEventKey(tcell.KeyHome, 0, 0))
	input.handle(tcell.NewEventKey(tcell.KeyRight, 0, 0))
	input.insert("中")
	input.handle(tcell.NewEventKey(tcell.KeyDelete, 0, 0))
	if input.String() != "A中e\u0301 long title" {
		t.Fatal(input.String())
	}
	input.handle(tcell.NewEventKey(tcell.KeyEnd, 0, 0))
	input.handle(tcell.NewEventKey(tcell.KeyCtrlW, 0, 0))
	if input.String() != "A中e\u0301 long " {
		t.Fatal(input.String())
	}
	text, cursor, _ := input.viewport(7)
	if cellWidth(text) > 7 || cursor >= 7 {
		t.Fatalf("viewport %q cursor %d", text, cursor)
	}
	if clip("界界abc", 5) != "界界a" || cellWidth(shorten("界界abc", 5)) != 5 {
		t.Fatal("wide text clipping")
	}
}

func TestScreenLayouts(t *testing.T) {
	for _, size := range [][2]int{{36, 12}, {46, 17}, {80, 24}, {120, 32}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			u, _ := testUI(t, size[0], size[1])
			_, err := u.store.Add("Review storage sizing and validate the rollout plan", testContext, "doing", "Keep the activity and space links", &Space{"w2", "Release", testContext.Session})
			if err != nil {
				t.Fatal(err)
			}
			drawUI(t, u)
			for _, want := range []string{"Tasks 1", "IN PROGRESS", "[>]", "↗ Release", "Enter Edit"} {
				if !strings.Contains(screenText(u), want) {
					t.Fatalf("missing %s\n%s", want, screenText(u))
				}
			}
			// Optional development artifact, never required by CI.
			if dir := os.Getenv("HERDR_TODO_TEST_FRAMES"); dir != "" {
				if err := os.WriteFile(fmt.Sprintf("%s/%dx%d.txt", dir, size[0], size[1]), []byte(screenText(u)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			key(t, u, tcell.KeyEnter)
			for _, want := range []string{"Edit task", "Planned", "Doing", "Done", "Space:", "Enter Save"} {
				if !strings.Contains(screenText(u), want) {
					t.Fatalf("missing editor %s\n%s", want, screenText(u))
				}
			}
		})
	}
}
