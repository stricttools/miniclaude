package keys

import (
	"testing"
)

func feedAll(p *Parser, chunks ...string) []Event {
	var out []Event
	for _, c := range chunks {
		out = append(out, p.Feed([]byte(c))...)
	}
	return out
}

func TestDecoding(t *testing.T) {
	cases := []struct {
		name  string
		input []string
		want  []Event
	}{
		{"runes", []string{"aé✨"}, []Event{{Key: KeyRune, Rune: 'a'}, {Key: KeyRune, Rune: 'é'}, {Key: KeyRune, Rune: '✨'}}},
		{"split rune", []string{"\xe2\x9c", "\xa8"}, []Event{{Key: KeyRune, Rune: '✨'}}},
		{"enter", []string{"\r\n"}, []Event{{Key: KeyEnter}, {Key: KeyEnter}}},
		{"alt enter", []string{"\x1b\r"}, []Event{{Key: KeyAltEnter}}},
		{"controls", []string{"\x01\x03\x04\x05\x0b\x15\x17\x7f\x08"}, []Event{{Key: KeyCtrlA}, {Key: KeyCtrlC}, {Key: KeyCtrlD}, {Key: KeyCtrlE}, {Key: KeyCtrlK}, {Key: KeyCtrlU}, {Key: KeyCtrlW}, {Key: KeyBackspace}, {Key: KeyBackspace}}},
		{"arrows", []string{"\x1b[A\x1b[B\x1b[C\x1b[D\x1bOH\x1b[F\x1b[3~"}, []Event{{Key: KeyUp}, {Key: KeyDown}, {Key: KeyRight}, {Key: KeyLeft}, {Key: KeyHome}, {Key: KeyEnd}, {Key: KeyDelete}}},
		{"modified arrow", []string{"\x1b[1;5A"}, []Event{{Key: KeyUp}}},
		{"split sequence", []string{"\x1b", "[", "A"}, []Event{{Key: KeyUp}}},
		{"wheel", []string{"\x1b[<64;10;5M\x1b[<65;10;5M\x1b[<0;1;1M\x1b[<68;1;1M"}, []Event{{Key: KeyWheelUp}, {Key: KeyWheelDown}, {Key: KeyWheelUp}}},
		{"paste", []string{"\x1b[200~line1\r\nline2\x1b", "[201~x"}, []Event{{Key: KeyPaste, Text: "line1\r\nline2"}, {Key: KeyRune, Rune: 'x'}}},
		{"tab ignored", []string{"\t"}, nil},
		{"unknown sequence ignored", []string{"\x1b[99zq"}, []Event{{Key: KeyRune, Rune: 'q'}}},
	}
	for _, c := range cases {
		var p Parser
		got := feedAll(&p, c.input...)
		if len(got) != len(c.want) {
			t.Errorf("%s: %+v", c.name, got)
			continue
		}
		for i := range got {
			if got[i].Key != c.want[i].Key || got[i].Rune != c.want[i].Rune || got[i].Text != c.want[i].Text {
				t.Errorf("%s: event %d %+v, want %+v", c.name, i, got[i], c.want[i])
			}
		}
	}
}

func TestALoneEscapeWaitsForTheTimeout(t *testing.T) {
	var p Parser
	if evs := p.Feed([]byte("\x1b")); len(evs) != 0 || !p.Waiting() {
		t.Fatalf("%+v %v", evs, p.Waiting())
	}
	evs := p.Flush()
	if len(evs) != 1 || evs[0].Key != KeyEscape {
		t.Fatalf("%+v", evs)
	}
	if evs := p.Feed([]byte("\x1bx")); len(evs) != 2 || evs[0].Key != KeyEscape || evs[1].Rune != 'x' {
		t.Fatalf("escape then x: %+v", evs)
	}
	p.Feed([]byte("\x1b[200~unfinished"))
	if p.Waiting() {
		t.Fatal("an unfinished paste waits on the timeout")
	}
}
