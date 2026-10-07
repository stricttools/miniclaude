// Package mock is a session that needs no Claude Code. It runs in one of
// two modes, chosen by its constructor:
//
//   - Scripted (NewScripted): each Send replays the next prepared list of
//     events, with no randomness and no timing; for tests.
//   - Live (NewLive): each Send reads a mock command from the prompt and
//     makes up a stream of events for it (text deltas, tool activity,
//     permission requests that wait for an answer, status events, and a
//     closing Result), timed like a stream. All randomness comes from
//     generators seeded from the seed, so a seed reproduces the same
//     content. It drives `miniclaude mock`.
//
// Both modes record the prompts sent and the calls made, for tests.
package mock

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/stricttools/claudestream"
	"github.com/stricttools/miniclaude/internal/session"
)

// Model is the model a live mock session reports.
const Model = "claude-mock"

// rateLimitSeedOffset seeds the rate-limit generator apart from the content
// generator, so emitting rate limits never changes the content.
const rateLimitSeedOffset = 0x5A17

// Call is one call made on the session, as a test may assert on it:
// "set_model" (model), "set_mode" (mode), "get_context_usage", "allow"
// (request ID, input, permissions), "deny" (request ID, message), or
// "cancelled" (request ID).
type Call struct {
	Method string
	Args   []any
}

// answerAction is how a request was answered.
type answerAction int

const (
	answerAllow answerAction = iota
	answerDeny
	answerCancelled
)

type answer struct {
	action  answerAction
	input   map[string]any
	message string
}

// pendingRequest is the request a live turn waits on.
type pendingRequest struct {
	id string
	// dialog is true for a UserDialogRequest, false for a
	// PermissionRequest.
	dialog bool
	// reply has room for the one answer.
	reply chan answer
}

// Session is the mock session.
type Session struct {
	live bool
	seed uint64
	cwd  string

	// interrupted stops the text stream of the live turn.
	interrupted atomic.Bool

	// The generators and errorPending are used by the live turn's
	// goroutine only, and turns never overlap.
	rng       *rand.Rand
	rateLimit *rand.Rand
	// errorPending makes the turn's Result an error and suppresses its
	// rate limits.
	errorPending bool

	mu             sync.Mutex
	turns          [][]claudestream.Event
	sent           []string
	calls          []Call
	interruptCount int
	model          string
	mode           string
	costUSD        float64
	tokens         int64
	turnCount      int
	contextTokens  int64
	firstSend      bool
	turnActive     bool
	unusable       bool
	closed         bool
	current        *liveTurn
	pending        *pendingRequest
}

var _ session.Session = (*Session)(nil)

// NewScripted returns a scripted session: each Send yields the next list of
// turns, and an empty turn once they run out. It reports the model
// "haiku", the mode "default", a cost of $0.5, 1234 tokens, and 3 turns.
func NewScripted(turns [][]claudestream.Event) *Session {
	return &Session{
		turns:     slices.Clone(turns),
		model:     "haiku",
		mode:      "default",
		costUSD:   0.5,
		tokens:    1234,
		turnCount: 3,
	}
}

// NewLive returns a live session seeded with seed. Its first turn starts
// with a SystemInit naming this process's working directory.
func NewLive(seed uint64) (*Session, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("reading the working directory for the mock session: %w", err)
	}
	return &Session{
		live:      true,
		seed:      seed,
		cwd:       cwd,
		rng:       rand.New(rand.NewPCG(seed, 0)),
		rateLimit: rand.New(rand.NewPCG(seed+rateLimitSeedOffset, 0)),
		model:     Model,
		mode:      "default",
		firstSend: true,
	}, nil
}

// Send opens a turn. It refuses as a claudestream session does: after
// Close, after a turn's reading was cancelled, and while a turn is open.
func (s *Session) Send(ctx context.Context, text string) (session.EventSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return nil, claudestream.ErrSessionClosed
	case s.unusable:
		return nil, claudestream.ErrSessionUnusable
	case s.turnActive:
		return nil, claudestream.ErrTurnActive
	}
	s.sent = append(s.sent, text)
	s.turnActive = true
	if !s.live {
		var events []claudestream.Event
		if len(s.turns) > 0 {
			events, s.turns = s.turns[0], s.turns[1:]
		}
		return &scriptedTurn{s: s, events: events}, nil
	}

	s.interrupted.Store(false)
	t := &liveTurn{s: s, events: make(chan claudestream.Event), stop: make(chan struct{})}
	s.current = t
	var systemInit *claudestream.SystemInit
	if s.firstSend {
		s.firstSend = false
		systemInit = &claudestream.SystemInit{
			EventHeader:    claudestream.EventHeader{SessionID: fmt.Sprintf("mock-%d", s.seed)},
			Cwd:            s.cwd,
			Model:          s.model,
			PermissionMode: s.mode,
		}
	}
	g := &generator{s: s, t: t}
	go g.run(text, systemInit)
	return t, nil
}

func (s *Session) endTurn(unusable bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnActive = false
	s.current = nil
	s.pending = nil
	if unusable {
		s.unusable = true
	}
}

func (s *Session) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// record notes a call and refuses it after Close.
func (s *Session) record(method string, args ...any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, Call{Method: method, Args: args})
	if s.closed {
		return claudestream.ErrSessionClosed
	}
	return nil
}

// Interrupt stops the live turn's text stream; the turn still ends with
// its rate limits and Result. Claude Code reports no queued messages.
func (s *Session) Interrupt(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, claudestream.ErrSessionClosed
	}
	s.interruptCount++
	s.interrupted.Store(true)
	return []string{}, nil
}

func (s *Session) SetModel(ctx context.Context, model string) error {
	if err := s.record("set_model", model); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.model = model
	return nil
}

func (s *Session) SetPermissionMode(ctx context.Context, mode string) error {
	if err := s.record("set_mode", mode); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = mode
	return nil
}

// GetContextUsage reports 100 of 1000 tokens, in two categories.
func (s *Session) GetContextUsage(ctx context.Context) (claudestream.ContextUsage, error) {
	if err := s.record("get_context_usage"); err != nil {
		return claudestream.ContextUsage{}, err
	}
	return claudestream.ContextUsage{
		TotalTokens: 100,
		MaxTokens:   1000,
		Percentage:  10,
		Categories: []claudestream.ContextCategory{
			{Name: "system", Tokens: 60},
			{Name: "messages", Tokens: 40},
		},
	}, nil
}

func (s *Session) RespondAllow(ctx context.Context, requestID string, input map[string]any, permissions []map[string]any) error {
	if err := s.record("allow", requestID, input, permissions); err != nil {
		return err
	}
	return s.respond(requestID, false, answer{action: answerAllow, input: input})
}

func (s *Session) RespondDeny(ctx context.Context, requestID, message string) error {
	if err := s.record("deny", requestID, message); err != nil {
		return err
	}
	return s.respond(requestID, false, answer{action: answerDeny, message: message})
}

func (s *Session) RespondDialogCancelled(ctx context.Context, requestID string) error {
	if err := s.record("cancelled", requestID); err != nil {
		return err
	}
	return s.respond(requestID, true, answer{action: answerCancelled})
}

// respond hands the answer to the live turn waiting on requestID. As in a
// claudestream session, an ID that is not waiting, or waiting for the other
// type of answer, is ErrUnknownRequest. A scripted session accepts every
// answer.
func (s *Session) respond(requestID string, dialog bool, a answer) error {
	if !s.live {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pending
	if p == nil || p.id != requestID || p.dialog != dialog {
		subtype := "can_use_tool"
		if dialog {
			subtype = "request_user_dialog"
		}
		return fmt.Errorf("%w: %s request %q", claudestream.ErrUnknownRequest, subtype, requestID)
	}
	s.pending = nil
	p.reply <- a
	return nil
}

func (s *Session) ModelName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.model
}

func (s *Session) PermissionMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

func (s *Session) TotalCostUSD() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.costUSD
}

func (s *Session) TotalTokens() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens
}

func (s *Session) TurnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnCount
}

// Close stops the live turn, if any; every later call is refused with
// claudestream.ErrSessionClosed.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.current != nil {
		s.current.halt()
	}
	return nil
}

// Sent lists the prompts sent, oldest first.
func (s *Session) Sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sent)
}

// Calls lists the calls made, oldest first.
func (s *Session) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// InterruptCount counts the calls to Interrupt.
func (s *Session) InterruptCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.interruptCount
}

// scriptedTurn replays prepared events.
type scriptedTurn struct {
	s      *Session
	events []claudestream.Event
	done   bool
}

func (t *scriptedTurn) Next(ctx context.Context) (claudestream.Event, error) {
	if t.done {
		return nil, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(t.events) == 0 {
		t.done = true
		t.s.endTurn(false)
		return nil, io.EOF
	}
	ev := t.events[0]
	t.events = t.events[1:]
	return ev, nil
}

// liveTurn receives the events its generator goroutine makes up.
type liveTurn struct {
	s *Session
	// events is closed by the generator when the turn ends or is stopped.
	events   chan claudestream.Event
	stop     chan struct{}
	stopOnce sync.Once
	done     bool
	failed   bool
}

func (t *liveTurn) halt() {
	t.stopOnce.Do(func() { close(t.stop) })
}

// Next returns the turn's next event. As in a claudestream session, ctx
// ending first stops the turn and makes the session unusable.
func (t *liveTurn) Next(ctx context.Context) (claudestream.Event, error) {
	if t.done {
		return nil, io.EOF
	}
	if t.failed {
		return nil, claudestream.ErrSessionUnusable
	}
	select {
	case ev, ok := <-t.events:
		if ok {
			return ev, nil
		}
		if t.s.isClosed() {
			return nil, claudestream.ErrSessionClosed
		}
		t.done = true
		t.s.endTurn(false)
		return nil, io.EOF
	case <-ctx.Done():
		t.failed = true
		t.halt()
		t.s.endTurn(true)
		return nil, ctx.Err()
	}
}
