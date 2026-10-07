package repl

import (
	"fmt"

	"github.com/stricttools/miniclaude/internal/input"
	"github.com/stricttools/miniclaude/internal/keys"
)

// modal is a question a turn asks the user, shown in the box in place of
// the input editor: a menu of choices (picked with the digits or the
// arrows, then Enter), or a line of text (Enter submits it). Escape or
// Ctrl+C dismisses it. The turn goroutine creates it, posts it to the
// controller, and waits on answer; from then on only the controller
// touches the other fields.
type modal struct {
	// message heads the box; empty shows no heading row.
	message string
	// choices are the menu's labels; nil for a text question.
	choices  []string
	selected int
	// editor holds the text of a text question.
	editor *input.Editor
	// answer receives the one answer; it has room for it, so answering
	// never blocks.
	answer chan modalAnswer
}

// modalAnswer is the user's answer to a modal.
type modalAnswer struct {
	// choice is the picked index of a menu.
	choice int
	// text is the text of a text question.
	text string
	// dismissed is true when the user pressed Escape or Ctrl+C.
	dismissed bool
}

func newChoiceModal(message string, choices []string) *modal {
	return &modal{message: message, choices: choices, answer: make(chan modalAnswer, 1)}
}

func newTextModal(message string) *modal {
	return &modal{message: message, editor: input.New(nil), answer: make(chan modalAnswer, 1)}
}

// openModal shows m in the box.
func (c *controller) openModal(m *modal) {
	c.modal = m
}

// closeModal removes m from the box when it is still there.
func (c *controller) closeModal(m *modal) {
	if c.modal == m {
		c.modal = nil
	}
}

// answerModal sends a to the turn waiting on the open modal and closes it.
func (c *controller) answerModal(a modalAnswer) {
	m := c.modal
	c.modal = nil
	m.answer <- a
}

// modalKey handles a key while a modal holds the box.
func (c *controller) modalKey(ev keys.Event) {
	m := c.modal
	switch ev.Key {
	case keys.KeyEscape, keys.KeyCtrlC:
		c.answerModal(modalAnswer{dismissed: true})
		return
	case keys.KeyEnter:
		if m.choices != nil {
			c.answerModal(modalAnswer{choice: m.selected})
		} else {
			c.answerModal(modalAnswer{text: m.editor.Text()})
		}
		return
	}
	if m.choices == nil {
		// A text question is one line: Alt+Enter inserts no newline.
		if ev.Key != keys.KeyAltEnter {
			m.editor.Apply(ev)
		}
		return
	}
	switch ev.Key {
	case keys.KeyUp:
		if m.selected > 0 {
			m.selected--
		}
	case keys.KeyDown:
		if m.selected < len(m.choices)-1 {
			m.selected++
		}
	case keys.KeyRune:
		if ev.Rune >= '1' && ev.Rune <= '9' {
			if n := int(ev.Rune - '0'); n <= len(m.choices) {
				m.selected = n - 1
			}
		}
	}
}

// render returns the box rows of the modal at width cells, and where the
// cursor shows: only a text question shows it.
func (m *modal) render(width int) (rows []string, showCursor bool, cursorRow, cursorCol int) {
	if m.message != "" {
		rows = append(rows, m.message)
	}
	if m.choices == nil {
		v := m.editor.Render(width)
		offset := len(rows)
		rows = append(rows, v.Rows...)
		return rows, true, offset + v.CursorRow, v.CursorCol
	}
	for i, label := range m.choices {
		marker := "  "
		if i == m.selected {
			marker = "❯ "
		}
		rows = append(rows, fmt.Sprintf("%s%d. %s", marker, i+1, label))
	}
	return rows, false, 0, 0
}
