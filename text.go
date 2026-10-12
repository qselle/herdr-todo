package main

import (
	"strings"
	"unicode"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"
)

func clean(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.C, r) {
			return ' '
		}
		return r
	}, text)
}

func cellWidth(s string) int { return uniseg.StringWidth(s) }

func clip(text string, cells int) string {
	var result strings.Builder
	used := 0
	g := uniseg.NewGraphemes(clean(text))
	for g.Next() {
		if used+g.Width() > cells {
			break
		}
		result.WriteString(g.Str())
		used += g.Width()
	}
	return result.String()
}

func shorten(text string, cells int) string {
	if cells < 1 {
		return ""
	}
	text = clean(text)
	if cellWidth(text) <= cells {
		return text
	}
	return clip(text, cells-1) + "…"
}

func wrapCells(text string, cells int) []string {
	if cells < 1 {
		return []string{""}
	}
	lines, line := []string{}, ""
	for _, word := range strings.Fields(clean(text)) {
		if line != "" && cellWidth(line+" "+word) > cells {
			lines = append(lines, line)
			line = ""
		}
		for cellWidth(word) > cells {
			part := clip(word, cells)
			if part == "" {
				g := uniseg.NewGraphemes(word)
				g.Next()
				part = g.Str()
			}
			lines = append(lines, part)
			word = strings.TrimPrefix(word, part)
		}
		if line != "" && word != "" {
			line += " "
		}
		line += word
	}
	if line != "" {
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

type TextInput struct {
	value         []rune
	cursor, limit int
}

func newInput(value string, limit int) *TextInput {
	runes := []rune(value)
	return &TextInput{runes, len(runes), max(limit, len(runes))}
}

func (t *TextInput) String() string { return string(t.value) }

func (t *TextInput) viewport(cells int) (string, int, int) {
	start := t.cursor
	for start > 0 && cellWidth(clean(string(t.value[start-1:t.cursor]))) < cells {
		start--
	}
	return clip(string(t.value[start:]), cells), cellWidth(clean(string(t.value[start:t.cursor]))), start
}

func (t *TextInput) click(cell, cells int) {
	text, _, start := t.viewport(cells)
	used, pos := 0, 0
	for _, r := range text {
		width := cellWidth(string(r))
		if float64(used)+float64(width)/2 > float64(cell) {
			break
		}
		used += width
		pos++
	}
	t.cursor = min(len(t.value), start+pos)
}

func (t *TextInput) insert(text string) {
	for _, r := range text {
		if !unicode.IsPrint(r) || unicode.Is(unicode.C, r) || len(t.value) >= t.limit {
			continue
		}
		t.value = append(t.value, 0)
		copy(t.value[t.cursor+1:], t.value[t.cursor:])
		t.value[t.cursor] = r
		t.cursor++
	}
}

func (t *TextInput) handle(key *tcell.EventKey) {
	switch key.Key() {
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if t.cursor > 0 {
			t.value = append(t.value[:t.cursor-1], t.value[t.cursor:]...)
			t.cursor--
		}
	case tcell.KeyDelete:
		if t.cursor < len(t.value) {
			t.value = append(t.value[:t.cursor], t.value[t.cursor+1:]...)
		}
	case tcell.KeyLeft:
		t.cursor = max(0, t.cursor-1)
	case tcell.KeyRight:
		t.cursor = min(len(t.value), t.cursor+1)
	case tcell.KeyHome, tcell.KeyCtrlA:
		t.cursor = 0
	case tcell.KeyEnd, tcell.KeyCtrlE:
		t.cursor = len(t.value)
	case tcell.KeyCtrlU:
		t.value, t.cursor = nil, 0
	case tcell.KeyCtrlW:
		pos := t.cursor
		for pos > 0 && unicode.IsSpace(t.value[pos-1]) {
			pos--
		}
		for pos > 0 && !unicode.IsSpace(t.value[pos-1]) {
			pos--
		}
		t.value, t.cursor = append(t.value[:pos], t.value[t.cursor:]...), pos
	case tcell.KeyRune:
		if key.Modifiers()&(tcell.ModCtrl|tcell.ModAlt) == 0 {
			t.insert(string(key.Rune()))
		}
	}
}
