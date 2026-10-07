// Package keys decodes the bytes a raw-mode terminal sends into key events.
// Parser is the pure decoder; Decoder runs it over a reader in background
// goroutines and delivers the events on a channel, so the controller never
// blocks on terminal input. A lone Escape is told apart from the start of
// an escape sequence by time: when nothing follows it within EscapeTimeout,
// it is the Escape key.
package keys

import (
	"bytes"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// EscapeTimeout is how long an incomplete escape sequence waits for more
// bytes before what was received is decoded as typed. A lone ESC byte that
// waits this long is the Escape key.
const EscapeTimeout = 50 * time.Millisecond

// Key names a decoded key.
type Key int

// The decoded keys.
const (
	// KeyRune is a printable character, in Event.Rune.
	KeyRune Key = iota
	// KeyEnter is Enter (CR, and LF as some terminals send).
	KeyEnter
	// KeyAltEnter is Enter with Alt (ESC CR).
	KeyAltEnter
	// KeyEscape is a lone Escape.
	KeyEscape
	KeyCtrlA
	KeyCtrlC
	KeyCtrlD
	KeyCtrlE
	KeyCtrlK
	KeyCtrlU
	KeyCtrlW
	KeyBackspace
	KeyDelete
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	// KeyWheelUp and KeyWheelDown are one mouse wheel step each.
	KeyWheelUp
	KeyWheelDown
	// KeyPaste is one bracketed paste, its text in Event.Text exactly as the
	// terminal sent it.
	KeyPaste
	// KeyReadError ends the event stream: reading the terminal failed with
	// Event.Err, and no events follow.
	KeyReadError
)

// Event is one decoded key.
type Event struct {
	Key  Key
	Rune rune
	Text string
	Err  error
}

// maxSequence bounds an escape sequence; a longer run without a final byte
// is not a sequence any terminal sends and is discarded.
const maxSequence = 64

// The bracketed paste markers.
var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
)

// Parser decodes terminal bytes into events. It keeps the bytes of an
// incomplete sequence until more arrive or Flush is called.
type Parser struct {
	buf []byte
}

// Feed appends data and returns every event that is complete.
func (p *Parser) Feed(data []byte) []Event {
	p.buf = append(p.buf, data...)
	var events []Event
	for len(p.buf) > 0 {
		ev, n, complete := decode(p.buf, false)
		if !complete {
			break
		}
		p.buf = p.buf[n:]
		events = append(events, ev...)
	}
	return events
}

// Waiting reports whether bytes of an incomplete sequence are held that
// Flush should decode after EscapeTimeout passes with no new bytes. An
// unfinished bracketed paste never waits on the timeout: it ends only at
// its end marker.
func (p *Parser) Waiting() bool {
	return len(p.buf) > 0 && !bytes.HasPrefix(p.buf, pasteStart)
}

// Flush decodes the held bytes as typed, without waiting for more: a lone
// ESC becomes Escape, and the bytes of an unfinished sequence after it are
// decoded on their own.
func (p *Parser) Flush() []Event {
	var events []Event
	for len(p.buf) > 0 && p.Waiting() {
		ev, n, _ := decode(p.buf, true)
		p.buf = p.buf[n:]
		events = append(events, ev...)
	}
	return events
}

// decode decodes the event at the start of buf. It returns the events (none
// for ignored input), the number of bytes consumed, and whether buf held a
// complete unit. When final is set, an incomplete unit is decoded as typed
// instead of reported incomplete.
func decode(buf []byte, final bool) ([]Event, int, bool) {
	b := buf[0]
	switch {
	case b == 0x1b:
		return decodeEscape(buf, final)
	case b == '\r' || b == '\n':
		return one(KeyEnter), 1, true
	case b == 0x7f || b == 0x08:
		return one(KeyBackspace), 1, true
	case b < 0x20:
		if k, ok := controlKeys[b]; ok {
			return one(k), 1, true
		}
		return nil, 1, true
	}
	if !utf8.FullRune(buf) && !final {
		return nil, 0, false
	}
	r, n := utf8.DecodeRune(buf)
	if r >= 0x80 && r < 0xa0 {
		return nil, n, true
	}
	return []Event{{Key: KeyRune, Rune: r}}, n, true
}

// controlKeys maps the control bytes that are keys of their own.
var controlKeys = map[byte]Key{
	0x01: KeyCtrlA,
	0x03: KeyCtrlC,
	0x04: KeyCtrlD,
	0x05: KeyCtrlE,
	0x0b: KeyCtrlK,
	0x15: KeyCtrlU,
	0x17: KeyCtrlW,
}

func one(k Key) []Event {
	return []Event{{Key: k}}
}

// decodeEscape decodes a unit starting with ESC.
func decodeEscape(buf []byte, final bool) ([]Event, int, bool) {
	if len(buf) == 1 {
		if final {
			return one(KeyEscape), 1, true
		}
		return nil, 0, false
	}
	switch buf[1] {
	case '\r':
		return one(KeyAltEnter), 2, true
	case '[':
		return decodeCSI(buf, final)
	case 'O':
		if len(buf) < 3 {
			if final {
				return one(KeyEscape), 1, true
			}
			return nil, 0, false
		}
		if k, ok := ss3Keys[buf[2]]; ok {
			return one(k), 3, true
		}
		return nil, 3, true
	}
	// ESC followed by anything else is Escape, then that key on its own.
	return one(KeyEscape), 1, true
}

// ss3Keys maps the final byte of an SS3 sequence (ESC O x).
var ss3Keys = map[byte]Key{
	'A': KeyUp,
	'B': KeyDown,
	'C': KeyRight,
	'D': KeyLeft,
	'H': KeyHome,
	'F': KeyEnd,
}

// csiFinalKeys maps the final byte of a CSI sequence whose meaning does not
// depend on its parameters (modifier parameters are ignored).
var csiFinalKeys = map[byte]Key{
	'A': KeyUp,
	'B': KeyDown,
	'C': KeyRight,
	'D': KeyLeft,
	'H': KeyHome,
	'F': KeyEnd,
}

// tildeKeys maps the first parameter of a CSI sequence ending in '~'.
var tildeKeys = map[string]Key{
	"1": KeyHome,
	"7": KeyHome,
	"4": KeyEnd,
	"8": KeyEnd,
	"3": KeyDelete,
}

// decodeCSI decodes a unit starting with ESC [.
func decodeCSI(buf []byte, final bool) ([]Event, int, bool) {
	if bytes.HasPrefix(buf, pasteStart) {
		return decodePaste(buf)
	}
	i := 2
	for i < len(buf) && i < maxSequence && buf[i] >= 0x20 && buf[i] <= 0x3f {
		i++
	}
	if i >= maxSequence {
		return nil, i, true
	}
	if i == len(buf) {
		if final {
			return one(KeyEscape), 1, true
		}
		return nil, 0, false
	}
	fin := buf[i]
	if fin < 0x40 || fin > 0x7e {
		// Not a well-formed sequence: drop what was read up to here.
		return nil, i, true
	}
	params := string(buf[2:i])
	n := i + 1
	if strings.HasPrefix(params, "<") && (fin == 'M' || fin == 'm') {
		return decodeMouse(params[1:], fin), n, true
	}
	if fin == '~' {
		first, _, _ := strings.Cut(params, ";")
		if k, ok := tildeKeys[first]; ok {
			return one(k), n, true
		}
		return nil, n, true
	}
	if k, ok := csiFinalKeys[fin]; ok {
		return one(k), n, true
	}
	return nil, n, true
}

// decodeMouse decodes an SGR mouse report "button;x;y" with final M (press)
// or m (release). Only wheel presses are events: button 64 is wheel up and
// 65 wheel down, with the Shift, Alt, and Ctrl bits (4, 8, 16) ignored.
func decodeMouse(params string, fin byte) []Event {
	if fin != 'M' {
		return nil
	}
	field, _, _ := strings.Cut(params, ";")
	button, err := strconv.Atoi(field)
	if err != nil {
		return nil
	}
	switch button &^ (4 | 8 | 16) {
	case 64:
		return one(KeyWheelUp)
	case 65:
		return one(KeyWheelDown)
	}
	return nil
}

// decodePaste decodes a bracketed paste. It is incomplete until the end
// marker arrives, however long that takes.
func decodePaste(buf []byte) ([]Event, int, bool) {
	body := buf[len(pasteStart):]
	end := bytes.Index(body, pasteEnd)
	if end < 0 {
		return nil, 0, false
	}
	text := strings.ToValidUTF8(string(body[:end]), "\uFFFD")
	n := len(pasteStart) + end + len(pasteEnd)
	return []Event{{Key: KeyPaste, Text: text}}, n, true
}
