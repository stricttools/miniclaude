package keys

import (
	"io"
	"time"
)

// Decoder reads a terminal in the background and delivers decoded events.
// One goroutine blocks on the reader; a second runs the Parser and the
// Escape timer, so a lone Escape is delivered EscapeTimeout after its byte
// arrived without anyone polling.
type Decoder struct {
	events chan Event
	done   chan struct{}
}

// chunk is one read from the reader: the bytes, or the error that ended
// reading.
type chunk struct {
	data []byte
	err  error
}

// Start starts decoding r. Reading stops at the first read error, which is
// delivered as a KeyReadError event before the events channel closes.
//
// A read cannot be interrupted, so after Stop the reading goroutine stays
// blocked in its read until the next byte arrives or the process exits; it
// delivers nothing after Stop.
func Start(r io.Reader) *Decoder {
	d := &Decoder{
		events: make(chan Event, 64),
		done:   make(chan struct{}),
	}
	chunks := make(chan chunk)
	go d.read(r, chunks)
	go d.decode(chunks)
	return d
}

// Events returns the channel of decoded events. It is closed after a
// KeyReadError event, and never closed by Stop.
func (d *Decoder) Events() <-chan Event {
	return d.events
}

// Stop stops delivering events. It must be called at most once.
func (d *Decoder) Stop() {
	close(d.done)
}

// read copies the reader's bytes onto chunks until a read fails.
func (d *Decoder) read(r io.Reader, chunks chan<- chunk) {
	for {
		buf := make([]byte, 4096)
		n, err := r.Read(buf)
		if n > 0 {
			select {
			case chunks <- chunk{data: buf[:n]}:
			case <-d.done:
				return
			}
		}
		if err != nil {
			select {
			case chunks <- chunk{err: err}:
			case <-d.done:
			}
			return
		}
	}
}

// decode runs the parser over the chunks and the Escape timer, and sends
// the events.
func (d *Decoder) decode(chunks <-chan chunk) {
	var p Parser
	timer := time.NewTimer(EscapeTimeout)
	timer.Stop()
	for {
		select {
		case <-d.done:
			timer.Stop()
			return
		case c := <-chunks:
			timer.Stop()
			if c.err != nil {
				events := append(p.Flush(), Event{Key: KeyReadError, Err: c.err})
				if d.send(events) {
					close(d.events)
				}
				return
			}
			if !d.send(p.Feed(c.data)) {
				return
			}
			if p.Waiting() {
				timer.Reset(EscapeTimeout)
			}
		case <-timer.C:
			if !d.send(p.Flush()) {
				return
			}
		}
	}
}

// send delivers events in order. It reports false when Stop was called.
func (d *Decoder) send(events []Event) bool {
	for _, ev := range events {
		select {
		case d.events <- ev:
		case <-d.done:
			return false
		}
	}
	return true
}
