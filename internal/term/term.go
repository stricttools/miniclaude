// Package term puts the controlling terminal into the state the fullscreen
// REPL draws in, and puts it back. Open switches the input to raw mode and
// writes the sequences that enter the alternate screen, hide the cursor,
// turn off autowrap, and turn on SGR mouse reporting and bracketed paste;
// Restore writes the sequences that undo them and restores the saved
// terminal state. Restore is idempotent, so it may run from a deferred call,
// from Guard on a panic, and from the normal exit path alike. Window size
// changes arrive as SIGWINCH through os/signal and are delivered on the
// channel Resized returns.
package term

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"

	"golang.org/x/sys/unix"
	xterm "golang.org/x/term"
)

// enterSequence enters the alternate screen (?1049), clears it, hides the
// cursor (?25), turns autowrap off (?7) so a row whose measured width is
// wrong is clipped instead of spilling into the next row, and turns on
// mouse button reporting (?1000) in SGR encoding (?1006) and bracketed
// paste (?2004).
const enterSequence = "\x1b[?1049h\x1b[H\x1b[2J\x1b[?25l\x1b[?7l\x1b[?1000h\x1b[?1006h\x1b[?2004h"

// leaveSequence undoes enterSequence in reverse order and resets the
// character attributes.
const leaveSequence = "\x1b[?2004l\x1b[?1006l\x1b[?1000l\x1b[0m\x1b[?7h\x1b[?25h\x1b[?1049l"

// Size is a terminal size in character cells.
type Size struct {
	Cols int
	Rows int
}

// Terminal is the controlling terminal while the fullscreen REPL runs.
type Terminal struct {
	in      *os.File
	out     *os.File
	saved   *xterm.State
	signals chan os.Signal
	resized chan struct{}
	done    chan struct{}

	once       sync.Once
	restoreErr error
}

// Open checks that in and out are terminals, switches in to raw mode, and
// writes the sequences that set up the fullscreen state to out. When
// writing fails after raw mode was entered, the terminal is restored before
// the error is returned.
func Open(in, out *os.File) (*Terminal, error) {
	if !xterm.IsTerminal(int(in.Fd())) {
		return nil, fmt.Errorf("term: %s is not a terminal", in.Name())
	}
	if !xterm.IsTerminal(int(out.Fd())) {
		return nil, fmt.Errorf("term: %s is not a terminal", out.Name())
	}
	saved, err := xterm.MakeRaw(int(in.Fd()))
	if err != nil {
		return nil, fmt.Errorf("term: entering raw mode: %w", err)
	}
	t := &Terminal{
		in:      in,
		out:     out,
		saved:   saved,
		signals: make(chan os.Signal, 1),
		resized: make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	if _, err := out.WriteString(enterSequence); err != nil {
		werr := fmt.Errorf("term: writing the setup sequences: %w", err)
		return nil, errors.Join(werr, t.Restore())
	}
	signal.Notify(t.signals, unix.SIGWINCH)
	go t.forwardResizes()
	return t, nil
}

// forwardResizes turns SIGWINCH signals into notifications on resized. A
// notification that is still unread absorbs later ones, so a burst of
// signals while the controller is busy becomes one redraw.
func (t *Terminal) forwardResizes() {
	for {
		select {
		case <-t.done:
			return
		case <-t.signals:
			select {
			case t.resized <- struct{}{}:
			default:
			}
		}
	}
}

// Resized returns the channel that receives a value after the window size
// changed. The receiver reads the new size with Size.
func (t *Terminal) Resized() <-chan struct{} {
	return t.resized
}

// Size returns the current size of the output terminal.
func (t *Terminal) Size() (Size, error) {
	cols, rows, err := xterm.GetSize(int(t.out.Fd()))
	if err != nil {
		return Size{}, fmt.Errorf("term: reading the window size: %w", err)
	}
	return Size{Cols: cols, Rows: rows}, nil
}

// Out returns the terminal's output file, the writer the screen draws to.
func (t *Terminal) Out() *os.File {
	return t.out
}

// Restore stops the resize notifications, writes the sequences that leave
// the fullscreen state, and restores the terminal state saved by Open. Only
// the first call does anything; every call returns the first call's error.
func (t *Terminal) Restore() error {
	t.once.Do(func() {
		signal.Stop(t.signals)
		close(t.done)
		var errs []error
		if _, err := t.out.WriteString(leaveSequence); err != nil {
			errs = append(errs, fmt.Errorf("term: writing the restore sequences: %w", err))
		}
		if err := xterm.Restore(int(t.in.Fd()), t.saved); err != nil {
			errs = append(errs, fmt.Errorf("term: restoring the terminal state: %w", err))
		}
		t.restoreErr = errors.Join(errs...)
	})
	return t.restoreErr
}

// Guard restores the terminal when the goroutine it is deferred in panics,
// then panics again with the same value, so the panic message is printed to
// a usable terminal. Use it as the first deferred call of every goroutine
// that may panic while the terminal is open:
//
//	defer t.Guard()
//
// The stack of the original panic is written to standard error after the
// restore, because the repeated panic reports its own stack. A restore error
// is written there too, since there is no caller left to return it to; a
// failing write to standard error has nowhere else to go.
func (t *Terminal) Guard() {
	r := recover()
	if r == nil {
		return
	}
	if err := t.Restore(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "miniclaude: %v\n", err)
	}
	_, _ = fmt.Fprintf(os.Stderr, "miniclaude: panic: %v\n%s", r, debug.Stack())
	panic(r)
}
