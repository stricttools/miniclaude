package input

import (
	"strings"
	"testing"

	"github.com/stricttools/miniclaude/internal/keys"
)

func typeText(e *Editor, text string) {
	for _, r := range text {
		e.Apply(keys.Event{Key: keys.KeyRune, Rune: r})
	}
}

func press(e *Editor, ks ...keys.Key) {
	for _, k := range ks {
		e.Apply(keys.Event{Key: k})
	}
}

func TestEditingKeys(t *testing.T) {
	e := New(nil)
	typeText(e, "hello world")
	press(e, keys.KeyCtrlW)
	if e.Text() != "hello " {
		t.Fatalf("Ctrl+W: %q", e.Text())
	}
	press(e, keys.KeyBackspace, keys.KeyCtrlA)
	typeText(e, ">")
	if e.Text() != ">hello" {
		t.Fatalf("Ctrl+A then type: %q", e.Text())
	}
	press(e, keys.KeyCtrlK)
	if e.Text() != ">" {
		t.Fatalf("Ctrl+K: %q", e.Text())
	}
	press(e, keys.KeyAltEnter)
	typeText(e, "second")
	press(e, keys.KeyHome, keys.KeyCtrlU)
	if e.Text() != ">second" {
		t.Fatalf("Ctrl+U at a line start joins the lines: %q", e.Text())
	}
	e.Apply(keys.Event{Key: keys.KeyPaste, Text: "a\r\nb\rc"})
	if e.Text() != ">a\nb\ncsecond" {
		t.Fatalf("paste: %q", e.Text())
	}
}

func TestSubmitAndHistory(t *testing.T) {
	e := New([]string{"old one", "old two"})
	typeText(e, "   ")
	if _, ok := e.Submit(); ok || e.Text() != "" {
		t.Fatal("blank text was submitted")
	}
	typeText(e, "new")
	sub, ok := e.Submit()
	if !ok || sub.Text != "new" || !sub.NewHistoryEntry {
		t.Fatalf("%+v %v", sub, ok)
	}
	typeText(e, "new")
	if sub, _ := e.Submit(); sub.NewHistoryEntry {
		t.Fatal("a repeat of the latest entry was recorded again")
	}
	press(e, keys.KeyUp)
	if e.Text() != "new" {
		t.Fatalf("up: %q", e.Text())
	}
	press(e, keys.KeyUp)
	if e.Text() != "old two" {
		t.Fatalf("up twice: %q", e.Text())
	}
	typeText(e, "!")
	press(e, keys.KeyDown, keys.KeyUp)
	if e.Text() != "old two!" {
		t.Fatalf("an edit to a recalled entry was lost: %q", e.Text())
	}
	press(e, keys.KeyDown, keys.KeyDown)
	if e.Text() != "" {
		t.Fatalf("down to the new line: %q", e.Text())
	}
}

func TestSuggestion(t *testing.T) {
	e := New([]string{"git status --short", "make test\nmake lint"})
	typeText(e, "make")
	if e.Suggestion() != " lint" {
		t.Fatalf("suggestion %q", e.Suggestion())
	}
	press(e, keys.KeyLeft)
	if e.Suggestion() != "" {
		t.Fatal("a suggestion is shown away from the end")
	}
	press(e, keys.KeyEnd, keys.KeyRight)
	if e.Text() != "make lint" {
		t.Fatalf("Right accepts: %q", e.Text())
	}
	e.Reset()
	typeText(e, "gi")
	press(e, keys.KeyCtrlE)
	if e.Text() != "git status --short" {
		t.Fatalf("Ctrl+E accepts: %q", e.Text())
	}
}

func TestRenderWrapsAndKeepsTheCursorVisible(t *testing.T) {
	e := New(nil)
	typeText(e, strings.Repeat("x", 25))
	v := e.Render(10)
	if len(v.Rows) != 3 || v.CursorRow != 2 || v.CursorCol != 5 {
		t.Fatalf("%+v", v)
	}
	e.Reset()
	for i := 0; i < 15; i++ {
		typeText(e, "line")
		press(e, keys.KeyAltEnter)
	}
	v = e.Render(20)
	if len(v.Rows) > MaxRows || v.CursorRow != len(v.Rows)-1 {
		t.Fatalf("rows %d cursor row %d", len(v.Rows), v.CursorRow)
	}
	e.Reset()
	typeText(e, "a\tb")
	if v := e.Render(20); v.Rows[0] != "a^Ib" {
		t.Fatalf("a tab is not drawn in caret notation: %q", v.Rows[0])
	}
}
