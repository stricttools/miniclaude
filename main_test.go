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

// fakeClaude is a stand-in for Claude Code speaking just enough of the
// stream-json protocol for one REPL turn: it answers the initialize
// handshake and, for each user message, streams one reply and a result.
const fakeClaude = `#!/bin/bash
if [ "$1" = "-v" ]; then echo "2.1.281 (Claude Code)"; exit 0; fi
printf '%s\n' "$*" > "$HOME/claude-args"
while IFS= read -r line; do
  case "$line" in
    *'"subtype":"initialize"'*)
      printf '%s\n' '{"type":"control_response","response":{"subtype":"success","response":{}}}' ;;
    *'"type":"user"'*)
      printf '%s\n' '{"type":"system","subtype":"init","cwd":"/w","session_id":"s1","model":"claude-fake","permissionMode":"default","uuid":"u"}'
      printf '%s\n' '{"type":"stream_event","session_id":"s1","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"fake-REPLY "}}}'
      printf '%s\n' '{"type":"stream_event","session_id":"s1","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"done\n"}}}'
      printf '%s\n' '{"type":"assistant","session_id":"s1","message":{"content":[{"type":"text","text":"fake-REPLY done\n"}]}}'
      printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"x","session_id":"s1","total_cost_usd":0.5,"duration_ms":1200,"usage":{"input_tokens":3,"output_tokens":4},"modelUsage":{"claude-fake":{"inputTokens":3,"contextWindow":100}}}' ;;
  esac
done
`

// fakeClaudewheel runs what follows "--" in "profile exec --name P -- ...".
const fakeClaudewheel = `#!/bin/sh
[ "$1 $2 $3" = "profile exec --name" ] || exit 2
shift 4
[ "$1" = "--" ] || exit 2
shift
exec "$@"
`

func TestReplRunsATurnOnAClaudeCodeSession(t *testing.T) {
	home := t.TempDir()
	claude, wheel := filepath.Join(home, "claude"), filepath.Join(home, "claudewheel")
	for path, body := range map[string]string{claude: fakeClaude, wheel: fakeClaudewheel} {
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(os.Args[0], "repl", "--profile", "work", "--model", "opus", "--permission-mode", "bypassPermissions",
		"--claude-binary", claude, "--claudewheel-binary", wheel)
	cmd.Dir = home
	cmd.Env = []string{childEnv + "=1", "HOME=" + home, "XDG_STATE_HOME=" + filepath.Join(home, "state"),
		"PATH=/usr/bin:/bin", "TERM=xterm-256color"}
	p := ptytest.StartPTY(t, cmd, 24, 80)
	p.Expect("┌", waitFor)
	p.Send("hello there\r")
	p.Expect("fake-REPLY done", waitFor)
	p.Expect("$0.5000 · 1.2s · 1 turn(s) · ctx 3%", waitFor)
	p.Send("/cost\r")
	p.Expect("7 tokens", waitFor)
	p.Send("/quit\r")
	if code := p.Wait(waitFor); code != 0 {
		t.Fatalf("exit %d; output:\n%q", code, p.Output())
	}
	args, err := os.ReadFile(filepath.Join(home, "claude-args"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--model opus", "--permission-mode bypassPermissions", "--permission-prompt-tool stdio"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("claude was started without %s: %s", want, args)
		}
	}
}

func runCLI(t *testing.T, home string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = home
	cmd.Env = []string{childEnv + "=1", "HOME=" + home, "XDG_STATE_HOME=" + filepath.Join(home, "state"), "PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

func TestHistoryImport(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "pt_history")
	data, err := os.ReadFile("internal/history/testdata/prompt_toolkit_history")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(home, "state", "miniclaude", "history.jsonl")
	out, code := runCLI(t, home, "history", "import", "--from", src, "--dry-run")
	if code != 0 || !strings.Contains(out, "would import 4 entries") {
		t.Fatalf("dry run: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("the dry run wrote the history")
	}
	out, code = runCLI(t, home, "history", "import", "--from", src)
	if code != 0 || !strings.Contains(out, "imported 4 entries") {
		t.Fatalf("import: exit %d\n%s", code, out)
	}
	info, err := os.Stat(dest)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("destination %v %v", info, err)
	}
	if _, code := runCLI(t, home, "history", "import", "--from", src); code != 2 {
		t.Fatalf("an existing destination: exit %d", code)
	}
	if _, code := runCLI(t, home, "history", "import", "--from", filepath.Join(home, "missing")); code == 0 {
		t.Fatal("a missing source was imported")
	}
}

func TestCommandLineRefusals(t *testing.T) {
	home := t.TempDir()
	cases := [][]string{
		{"repl", "--profile", "p", "--model", "m", "--permission-mode", "bypassPermissions"},
		{"repl", "--profile", "p", "--model", "m", "--permission-mode", "yolo"},
		{"repl", "--profile", "p", "--permission-mode", "default"},
		{"repl", "--profile", "p", "--model", "m", "--permission-mode", "default", "--json"},
		{"repl", "--profile", "p", "--model", "m", "--permission-mode", "default", "--dry-run"},
		{"mock", "--seed", "-1"},
		{"mock", "--json"},
	}
	for _, args := range cases {
		if out, code := runCLI(t, home, args...); code == 0 {
			t.Errorf("%v accepted:\n%s", args, out)
		}
	}
	if out, code := runCLI(t, home, "--version"); code != 0 || !strings.Contains(out, "miniclaude") {
		t.Errorf("--version: exit %d %s", code, out)
	}
}
