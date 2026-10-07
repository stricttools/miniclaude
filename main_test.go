package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stricttools/miniclaude/internal/ptytest"
)

// childEnv makes the test binary run miniclaude's command line instead of
// the tests, so a test can drive the REPL on a pseudo-terminal.
const childEnv = "MINICLAUDE_TEST_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

const waitFor = 15 * time.Second

// startMock starts `miniclaude mock --seed 7` on a rows by cols terminal in a
// throwaway home, with no howmuchleft on PATH.
func startMock(t *testing.T, rows, cols uint16) (*ptytest.PTY, string) {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "mock", "--seed", "7")
	cmd.Dir = home
	cmd.Env = []string{childEnv + "=1", "HOME=" + home, "XDG_STATE_HOME=" + filepath.Join(home, "state"),
		"PATH=/usr/bin:/bin", "TERM=xterm-256color", "LANG=C.UTF-8"}
	p := ptytest.StartPTY(t, cmd, rows, cols)
	p.Expect("┌", waitFor)
	return p, home
}

// screen replays everything the child wrote onto a screen model.
func screen(p *ptytest.PTY, rows, cols int) *ptytest.Screen {
	s := ptytest.NewScreen(rows, cols)
	s.Feed(p.Output())
	return s
}

func TestMockTurnStreamsBackAndQuits(t *testing.T) {
	p, home := startMock(t, 24, 80)
	p.Send("md hello-MARKER\r")
	p.Expect("hello-MARKER", waitFor)
	p.Send("/quit\r")
	if code := p.Wait(waitFor); code != 0 {
		t.Fatalf("exit %d; output:\n%q", code, p.Output())
	}
	history, err := os.ReadFile(filepath.Join(home, "state", "miniclaude", "history.jsonl"))
	if err != nil || !strings.Contains(string(history), `"md hello-MARKER"`) {
		t.Fatalf("history %q %v", history, err)
	}
}

func TestLayoutIsAnchoredToTheBottom(t *testing.T) {
	for _, size := range [][2]int{{24, 80}, {12, 40}, {40, 120}} {
		rows, cols := size[0], size[1]
		p, _ := startMock(t, uint16(rows), uint16(cols))
		p.Send("md anchored\r")
		p.Expect("anchored", waitFor)
		lines := screen(p, rows, cols).Lines()
		bottomOfBox := -1
		for i, line := range lines {
			if strings.HasPrefix(line, "└") {
				bottomOfBox = i
			}
		}
		if bottomOfBox != rows-1-3 {
			t.Errorf("%dx%d: the box ends on row %d:\n%s", rows, cols, bottomOfBox, strings.Join(lines, "\n"))
		}
		if !strings.HasPrefix(lines[bottomOfBox-2], "┌") {
			t.Errorf("%dx%d: the one-line box does not start two rows above its end:\n%s", rows, cols, strings.Join(lines, "\n"))
		}
		p.Send("\x04")
		if code := p.Wait(waitFor); code != 0 {
			t.Fatalf("Ctrl+D: exit %d", code)
		}
	}
}

func TestEveryMockSectionRunsAtSmallAndLargeSizes(t *testing.T) {
	for _, size := range [][2]int{{12, 40}, {30, 120}} {
		rows, cols := size[0], size[1]
		p, _ := startMock(t, uint16(rows), uint16(cols))
		for _, section := range []string{"help", "table", "text", "thinking", "tools", "status", "error"} {
			before := len(p.Output())
			p.Send(section + "\r")
			p.ExpectAfter(before, "turn(s)", waitFor)
		}
		lines := screen(p, rows, cols).Lines()
		if !strings.HasPrefix(lines[rows-1-3], "└") {
			t.Errorf("%dx%d: the layout moved:\n%s", rows, cols, strings.Join(lines, "\n"))
		}
		p.Send("/quit\r")
		if code := p.Wait(waitFor); code != 0 {
			t.Fatalf("exit %d", code)
		}
	}
}

func TestDialogsAreAnsweredThroughModals(t *testing.T) {
	p, _ := startMock(t, 30, 100)
	p.Send("dialogs\r")
	p.Expect("Allow once", waitFor)
	p.Send("\r") // the highlighted first choice: allow once
	p.Expect("Which color do you prefer?", waitFor)
	at := len(p.Output())
	p.Send("2\r") // Blue
	p.ExpectAfter(at, "turn(s)", waitFor)
	if out := p.Output()[at:]; !strings.Contains(out, "Blue") {
		t.Errorf("the answer is not shown:\n%q", out)
	}
	p.Send("/quit\r")
	if code := p.Wait(waitFor); code != 0 {
		t.Fatalf("exit %d; output:\n%q", code, p.Output())
	}
}

func TestEscapeInterruptsASlowTurnAndTypedPromptsQueue(t *testing.T) {
	p, _ := startMock(t, 24, 80)
	p.Send("slow\r")
	time.Sleep(300 * time.Millisecond)
	p.Send("md queued-MARKER\r")
	p.Expect("queued: md queued-MARKER", waitFor)
	p.Send("\x1b")
	p.Expect("queued-MARKER", waitFor)
	p.Send("/quit\r")
	if code := p.Wait(waitFor); code != 0 {
		t.Fatalf("exit %d; output:\n%q", code, p.Output())
	}
}

func TestCtrlCClearsTheInputAndCtrlDExits(t *testing.T) {
	p, _ := startMock(t, 24, 80)
	p.Send("half-typed")
	p.Expect("half-typed", waitFor)
	p.Send("\x03")
	time.Sleep(200 * time.Millisecond)
	if strings.Contains(screen(p, 24, 80).Text(), "half-typed") {
		t.Fatal("Ctrl+C did not clear the input")
	}
	p.Send("\x04")
	if code := p.Wait(waitFor); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if out := p.Output(); !strings.Contains(out, "\x1b[?1049l") {
		t.Fatal("the alternate screen was not left")
	}
}

func TestResizeRedrawsAtTheNewWidth(t *testing.T) {
	p, _ := startMock(t, 30, 120)
	p.Send("md | Name | Description |\n")
	p.Send("\x1b\r") // Alt+Enter: a new line in the editor
	p.Send("| --- | --- |\x1b\r| Alice | A rather long description that has to wrap when narrow |\r")
	p.Expect("Alice", waitFor)
	p.Resize(30, 50)
	time.Sleep(500 * time.Millisecond)
	lines := screen(p, 30, 120).Lines()
	for _, line := range lines {
		if len([]rune(line)) > 50 {
			t.Fatalf("a row is wider than the new width:\n%s", strings.Join(lines, "\n"))
		}
	}
	p.Send("/quit\r")
	if code := p.Wait(waitFor); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestRefusesWithoutATerminal(t *testing.T) {
	cmd := exec.Command(os.Args[0], "mock")
	cmd.Env = append(os.Environ(), childEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "needs a terminal") {
		t.Fatalf("%v\n%s", err, out)
	}
}
