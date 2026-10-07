// Package repl is the fullscreen REPL: the output region, the input box,
// and the howmuchleft status rows, driven by one controller goroutine that
// owns all of the REPL's state. The controller selects over the decoded
// keys, window resizes, a 250 ms tick, the closures other goroutines post
// to it (turn output, modal requests, answers of session calls), the
// howmuchleft results, and the termination signals. It does no blocking
// work beyond drawing: turns, slash commands that call the session, the
// interrupt, the history file writes, and howmuchleft run on goroutines of
// their own and report back by posting closures.
//
// The terminal is restored on every way out: a normal return, an error, a
// panic in the controller or in any goroutine it starts (each defers the
// terminal's Guard), and SIGINT, SIGTERM, or SIGHUP.
package repl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/stricttools/miniclaude/internal/displaytext"
	"github.com/stricttools/miniclaude/internal/history"
	"github.com/stricttools/miniclaude/internal/howmuchleft"
	"github.com/stricttools/miniclaude/internal/input"
	"github.com/stricttools/miniclaude/internal/keys"
	"github.com/stricttools/miniclaude/internal/output"
	"github.com/stricttools/miniclaude/internal/screen"
	"github.com/stricttools/miniclaude/internal/session"
	"github.com/stricttools/miniclaude/internal/term"
	"github.com/stricttools/miniclaude/internal/usageformat"
)

// tickInterval redraws the screen while nothing else happens, as the
// Python REPL's refresh interval did.
const tickInterval = 250 * time.Millisecond

// interruptTimeout limits the wait for Claude Code to answer an interrupt.
const interruptTimeout = 30 * time.Second

// historyBacklog is how many submitted lines may wait for the history
// writer before a submit waits for it.
const historyBacklog = 256

// updateBatch is how many posted closures the controller runs before it
// draws, so a fast stream is drawn in batches rather than once per delta.
const updateBatch = 256

const reset = "\x1b[0m"

func dim(text string) string { return "\x1b[2m" + text + reset }

func red(text string) string { return "\x1b[31m" + text + reset }

func yellow(text string) string { return "\x1b[33m" + text + reset }

// errorLine is the red line reporting err in the output.
func errorLine(err error) string {
	return red("error: "+err.Error()) + "\n"
}

// Config is what the REPL runs with.
type Config struct {
	// In and Out are the terminal.
	In, Out *os.File
	// Session is the conversation; the caller starts and closes it.
	Session session.Session
	// Model and PermissionMode are the values the session was started
	// with, shown until the session reports its own.
	Model          string
	PermissionMode string
	// Cwd is the working directory howmuchleft is told about until the
	// session reports its own.
	Cwd string
	// Intro, when not empty, is the first output line, in dim.
	Intro string
	// HistoryPath is the history file submitted lines are appended to, and
	// History its entries, oldest first.
	HistoryPath string
	History     []string
	// Cancel, when it closes, ends the REPL as a termination signal does;
	// nil never closes.
	Cancel <-chan struct{}
}

// Outcome is how the REPL ended.
type Outcome struct {
	// Summary is the session's cost line, for the normal screen; empty
	// when the REPL ended before the terminal was set up.
	Summary string
	// Signal is the termination signal that ended the REPL, nil when it
	// ended otherwise.
	Signal os.Signal
}

// controller is the REPL's state. Every field is read and written on the
// controller goroutine only, except the fields set before the loop starts
// and never changed afterwards (cfg, sess, term, updates, stopped, base).
type controller struct {
	cfg  Config
	sess session.Session
	term *term.Terminal
	scr  *screen.Screen
	dec  *keys.Decoder
	out  *output.Buffer
	ed   *input.Editor

	status     *howmuchleft.Runner
	statusRows []string

	// base is cancelled when the REPL ends, which stops every session call
	// still running.
	base       context.Context
	cancelBase context.CancelFunc
	// updates carries closures that other goroutines post for the
	// controller to run; stopped closes when the controller stops reading
	// it, so a post never blocks after that.
	updates chan func(*controller)
	stopped chan struct{}

	histLines  chan string
	histResult chan error

	signals   chan os.Signal
	hintTimer *time.Timer

	cols, rows int

	// queue holds submitted lines waiting for the turn or the slash
	// command in progress to end. busy is true while one is in progress;
	// turnActive while it is a turn.
	queue      []string
	busy       bool
	turnActive bool
	turnSeq    int
	turnCancel context.CancelFunc
	// interruptPending is true from an interrupt until the turn's result.
	interruptPending bool
	// unusable is set when an interrupt got no answer in time.
	unusable bool
	modal    *modal

	exit   bool
	signal os.Signal
	fatal  error

	// State reported by the session, for the status rows.
	cwd        string
	sessionID  string
	costUSD    float64
	ctxPct     *float64
	rateLimits map[string]howmuchleft.RateLimit
}

// Run runs the REPL on the terminal until the user leaves it, a
// termination signal arrives, cfg.Cancel closes, or something fails. The
// terminal is restored before Run returns.
func Run(cfg Config) (outcome Outcome, err error) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)

	t, err := term.Open(cfg.In, cfg.Out)
	if err != nil {
		return Outcome{}, err
	}
	defer t.Guard()
	// Restore is idempotent; this covers the returns before the explicit
	// restore below.
	defer func() {
		if rerr := t.Restore(); rerr != nil && err == nil {
			err = rerr
		}
	}()

	size, err := t.Size()
	if err != nil {
		return Outcome{}, err
	}

	base, cancelBase := context.WithCancel(context.Background())
	c := &controller{
		cfg:        cfg,
		sess:       cfg.Session,
		term:       t,
		scr:        screen.New(t.Out()),
		out:        output.New(),
		ed:         input.New(cfg.History),
		base:       base,
		cancelBase: cancelBase,
		updates:    make(chan func(*controller), 64),
		stopped:    make(chan struct{}),
		histLines:  make(chan string, historyBacklog),
		histResult: make(chan error, 1),
		signals:    signals,
		cols:       size.Cols,
		rows:       size.Rows,
		cwd:        cfg.Cwd,
	}
	c.status = howmuchleft.New(c.statusInput())
	if cfg.Intro != "" {
		c.out.Print(dim(cfg.Intro) + "\n")
	}

	c.dec = keys.Start(cfg.In)
	go func() {
		defer t.Guard()
		c.status.Run()
	}()
	go func() {
		defer t.Guard()
		c.writeHistory()
	}()

	loopErr := c.loop()
	shutdownErr := c.shutdown()
	restoreErr := t.Restore()

	outcome = Outcome{
		Summary: usageformat.CostLine(c.sess.TotalCostUSD(), c.sess.TotalTokens(), c.sess.TurnCount()),
		Signal:  c.signal,
	}
	return outcome, errors.Join(loopErr, shutdownErr, restoreErr)
}

// shutdown stops everything the controller started and waits for the
// history writer to finish the lines already submitted.
func (c *controller) shutdown() error {
	close(c.stopped)
	c.cancelBase()
	c.dec.Stop()
	c.status.Stop()
	c.hintTimerStop()
	close(c.histLines)
	return <-c.histResult
}

func (c *controller) hintTimerStop() {
	if c.hintTimer != nil {
		c.hintTimer.Stop()
	}
}

// writeHistory appends the submitted lines to the history file, in order,
// until histLines closes. A failed append is reported in the output and
// in the error it delivers on histResult at the end.
func (c *controller) writeHistory() {
	var errs []error
	for text := range c.histLines {
		if err := history.Append(c.cfg.HistoryPath, text); err != nil {
			errs = append(errs, err)
			post(c.updates, c.stopped, func(c *controller) { c.out.Print(errorLine(err)) })
		}
	}
	c.histResult <- errors.Join(errs...)
}

// post hands f to the controller. It reports false, without running f,
// once the controller has stopped.
func post(updates chan<- func(*controller), stopped <-chan struct{}, f func(*controller)) bool {
	select {
	case updates <- f:
		return true
	case <-stopped:
		return false
	}
}

// goGuarded runs fn on a new goroutine that restores the terminal if fn
// panics.
func (c *controller) goGuarded(fn func()) {
	go func() {
		defer c.term.Guard()
		fn()
	}()
}

// loop runs until the REPL is left, drawing after every event.
func (c *controller) loop() error {
	tick := time.NewTicker(tickInterval)
	defer tick.Stop()
	c.hintTimer = time.NewTimer(time.Hour)
	c.hintTimer.Stop()

	if err := c.draw(); err != nil {
		return err
	}
	for !c.exit {
		select {
		case ev, ok := <-c.dec.Events():
			if !ok {
				c.exit = true
				break
			}
			c.key(ev)
		case <-c.term.Resized():
			size, err := c.term.Size()
			if err != nil {
				return err
			}
			c.cols, c.rows = size.Cols, size.Rows
		case <-tick.C:
		case <-c.hintTimer.C:
		case rows := <-c.status.Results():
			c.statusRows = rows
		case f := <-c.updates:
			f(c)
			c.runPosted()
		case s := <-c.signals:
			c.signal = s
			c.exit = true
		case <-c.cfg.Cancel:
			c.exit = true
		}
		if c.fatal != nil {
			return c.fatal
		}
		if c.exit {
			break
		}
		c.status.Set(c.statusInput(), c.turnActive)
		if err := c.draw(); err != nil {
			return err
		}
	}
	return nil
}

// runPosted runs closures already posted, up to updateBatch, without
// waiting for more.
func (c *controller) runPosted() {
	for range updateBatch {
		select {
		case f := <-c.updates:
			f(c)
		default:
			return
		}
	}
}

// key handles one decoded key.
func (c *controller) key(ev keys.Event) {
	switch ev.Key {
	case keys.KeyReadError:
		if errors.Is(ev.Err, io.EOF) {
			c.exit = true
		} else {
			c.fatal = fmt.Errorf("reading the terminal: %w", ev.Err)
		}
		return
	case keys.KeyWheelUp:
		c.out.WheelUp(time.Now())
		c.armHintTimer()
		return
	case keys.KeyWheelDown:
		c.out.WheelDown(time.Now())
		c.armHintTimer()
		return
	}
	if c.modal != nil {
		c.modalKey(ev)
		return
	}
	switch ev.Key {
	case keys.KeyEnter:
		c.submit()
	case keys.KeyEscape:
		if c.turnActive {
			c.interrupt()
		}
	case keys.KeyCtrlC:
		if c.turnActive {
			c.interrupt()
		} else {
			c.ed.Reset()
		}
	case keys.KeyCtrlD:
		if c.ed.Text() == "" {
			c.exit = true
		}
	default:
		c.ed.Apply(ev)
	}
}

// armHintTimer schedules a redraw for when the boundary hint a wheel event
// may have shown disappears.
func (c *controller) armHintTimer() {
	if wait := time.Until(c.out.HintExpiry()); wait > 0 {
		c.hintTimer.Reset(wait + 10*time.Millisecond)
	}
}

// submit takes the editor's text: it is recorded in the history file when
// it is a new entry, and queued to run once the turn or slash command in
// progress ends.
func (c *controller) submit() {
	sub, ok := c.ed.Submit()
	if !ok {
		return
	}
	if sub.NewHistoryEntry {
		c.histLines <- sub.Text
	}
	c.out.ResetScroll()
	if c.busy {
		c.out.Print(dim("queued: "+snippet(sub.Text)) + "\n")
	}
	c.queue = append(c.queue, sub.Text)
	c.next()
}

// snippet is text with its whitespace runs collapsed to single spaces, cut
// to 40 characters.
func snippet(text string) string {
	s := []rune(strings.Join(strings.FieldsFunc(text, displaytext.IsSpace), " "))
	if len(s) > 40 {
		s = s[:40]
	}
	return string(s)
}

// next runs queued lines until one starts a turn or a session call, or the
// queue is empty.
func (c *controller) next() {
	for !c.busy && !c.exit && len(c.queue) > 0 {
		line := c.queue[0]
		c.queue = c.queue[1:]
		if c.slashCommand(line) {
			continue
		}
		c.startTurn(line)
	}
}

// interrupt asks Claude Code to stop the running turn, once per turn until
// its result. With no answer within interruptTimeout, the session is
// treated as unusable: the turn's reading is cancelled, which makes the
// session refuse further turns.
func (c *controller) interrupt() {
	if c.interruptPending || !c.turnActive {
		return
	}
	c.interruptPending = true
	c.out.Print(dim("interrupting…") + "\n")
	seq := c.turnSeq
	sess := c.sess
	updates, stopped, base := c.updates, c.stopped, c.base
	c.goGuarded(func() {
		ctx, cancel := context.WithTimeout(base, interruptTimeout)
		defer cancel()
		_, err := sess.Interrupt(ctx)
		timedOut := err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)
		post(updates, stopped, func(c *controller) { c.interruptAnswered(seq, err, timedOut) })
	})
}

// interruptAnswered reports the end of an interrupt of turn seq.
func (c *controller) interruptAnswered(seq int, err error, timedOut bool) {
	switch {
	case timedOut:
		c.out.Print(red(fmt.Sprintf("interrupt got no answer in %ds; the session is unusable", int(interruptTimeout/time.Second))) + "\n")
		c.unusable = true
		if c.turnActive && c.turnSeq == seq {
			c.turnCancel()
		}
	case err != nil && c.base.Err() == nil:
		c.out.Print(errorLine(fmt.Errorf("interrupting the turn: %w", err)))
	}
}

// modelName is the session's model, or the one it was started with while
// the session has not reported one.
func (c *controller) modelName() string {
	if m := c.sess.ModelName(); m != "" {
		return m
	}
	return c.cfg.Model
}

// modeName is the session's permission mode, or the one it was started
// with while the session has not reported one.
func (c *controller) modeName() string {
	if m := c.sess.PermissionMode(); m != "" {
		return m
	}
	return c.cfg.PermissionMode
}

// statusInput is what howmuchleft is told.
func (c *controller) statusInput() howmuchleft.Input {
	return howmuchleft.Input{
		Model:          c.modelName(),
		Cwd:            c.cwd,
		CostUSD:        c.costUSD,
		SessionID:      c.sessionID,
		ContextPercent: c.ctxPct,
		RateLimits:     c.rateLimits,
	}
}

// draw draws the frame: the output rows, the box holding the modal or the
// editor, and the status rows.
func (c *controller) draw() error {
	inner := max(1, c.cols-2)
	var box []string
	showCursor := false
	cursorRow, cursorCol := 0, 0
	if c.modal != nil {
		box, showCursor, cursorRow, cursorCol = c.modal.render(inner)
	} else {
		v := c.ed.Render(inner)
		box, showCursor, cursorRow, cursorCol = v.Rows, true, v.CursorRow, v.CursorCol
	}
	c.out.Layout(c.cols, screen.OutputHeight(c.rows, len(box)))
	top, bottom := c.out.Hints(time.Now())
	return c.scr.Draw(screen.Frame{
		Output:     c.out.Visible(),
		TopHint:    top,
		BottomHint: bottom,
		Box:        box,
		ShowCursor: showCursor,
		CursorRow:  cursorRow,
		CursorCol:  cursorCol,
		Status:     c.statusRows,
	}, c.cols, c.rows)
}
