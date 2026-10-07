// Package howmuchleft runs the howmuchleft program for the REPL's status
// rows. howmuchleft takes no arguments, reads a JSON description of the
// session on stdin (model, working directory, cost, session ID, context
// window use, and rate limits), and prints the status rows with their own
// colors.
//
// A Runner queries it in the background, as often as the REPL's state asks
// for (every BusyInterval during a turn, every IdleInterval otherwise), each
// run limited to Timeout, and delivers each result on a channel. A result
// is always the outcome of the latest run: when howmuchleft is missing from
// PATH the rows say so, and when a run fails the rows are one red line
// describing the failure, never the output of an earlier run.
package howmuchleft

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Program is the name looked up on PATH before every run, so installing
// howmuchleft while the REPL runs takes effect at the next run.
const Program = "howmuchleft"

// Timeout limits one run of howmuchleft.
const Timeout = 500 * time.Millisecond

// BusyInterval and IdleInterval separate the end of one run from the start
// of the next, during a turn and outside one.
const (
	BusyInterval = 250 * time.Millisecond
	IdleInterval = time.Second
)

// waitDelay bounds the wait for howmuchleft's output pipes after it was
// killed, in case a child it started still holds them open.
const waitDelay = 100 * time.Millisecond

// NotInstalled are the status rows shown when howmuchleft is not on PATH.
var NotInstalled = []string{
	"howmuchleft not installed",
	"howmuchleft not installed",
	"howmuchleft not installed",
}

// RateLimit is one rate limit's state, as howmuchleft reads it.
type RateLimit struct {
	UsedPercentage float64
	// ResetsAt is a Unix time in seconds; nil when not reported.
	ResetsAt *int64
}

// Input is what howmuchleft is told about the session.
type Input struct {
	// Model is the session's model; empty is sent as "?".
	Model string
	Cwd   string
	// CostUSD is the session's total cost so far.
	CostUSD float64
	// SessionID is omitted when empty.
	SessionID string
	// ContextPercent is the share of the context window in use; nil when
	// not known yet.
	ContextPercent *float64
	// RateLimits are keyed by the rate limit's type ("five_hour" and so
	// on); omitted when empty.
	RateLimits map[string]RateLimit
}

// MarshalStdin returns the JSON document howmuchleft reads on stdin.
func (in Input) MarshalStdin() ([]byte, error) {
	model := in.Model
	if model == "" {
		model = "?"
	}
	contextWindow := map[string]any{}
	if in.ContextPercent != nil {
		contextWindow["used_percentage"] = *in.ContextPercent
	}
	doc := map[string]any{
		"model":          model,
		"cwd":            in.Cwd,
		"cost":           map[string]any{"total_cost_usd": in.CostUSD},
		"context_window": contextWindow,
	}
	if in.SessionID != "" {
		doc["session_id"] = in.SessionID
	}
	if len(in.RateLimits) > 0 {
		limits := make(map[string]any, len(in.RateLimits))
		for name, limit := range in.RateLimits {
			entry := map[string]any{"used_percentage": limit.UsedPercentage}
			if limit.ResetsAt != nil {
				entry["resets_at"] = *limit.ResetsAt
			}
			limits[name] = entry
		}
		doc["rate_limits"] = limits
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encoding the howmuchleft input: %w", err)
	}
	return data, nil
}

// failure is the status row describing a failed run, in red.
func failure(err error) []string {
	return []string{"\x1b[31mhowmuchleft: " + err.Error() + "\x1b[0m"}
}

// Query runs howmuchleft once with in and returns its rows: what it printed,
// trailing newlines removed, split into lines; NotInstalled when it is not on
// PATH; or one red line describing the failure. It returns nil when ctx
// ended before the run did.
func Query(ctx context.Context, in Input) []string {
	path, err := exec.LookPath(Program)
	if errors.Is(err, exec.ErrNotFound) {
		return NotInstalled
	}
	if err != nil {
		return failure(fmt.Errorf("looking it up on PATH: %w", err))
	}
	stdin, err := in.MarshalStdin()
	if err != nil {
		return failure(err)
	}
	runCtx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, path)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = waitDelay
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil
	}
	if runErr != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return failure(fmt.Errorf("no answer in %v", Timeout))
		}
		if detail := firstLine(stderr.String()); detail != "" {
			return failure(fmt.Errorf("%w: %s", runErr, detail))
		}
		return failure(runErr)
	}
	out := strings.TrimRight(stdout.String(), "\n")
	if strings.TrimSpace(out) == "" {
		return failure(errors.New("printed nothing"))
	}
	return strings.Split(out, "\n")
}

// firstLine returns the first non-blank line of s, trimmed.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// Runner queries howmuchleft in the background. Set is called by the
// REPL's controller; Run runs on a goroutine of its own until Stop.
type Runner struct {
	mu    sync.Mutex
	input Input
	busy  bool

	wake    chan struct{}
	results chan []string

	ctx      context.Context
	cancel   context.CancelFunc
	stopOnce sync.Once
}

// New returns a runner that starts with in, outside a turn.
func New(in Input) *Runner {
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{
		input:   cloneInput(in),
		wake:    make(chan struct{}, 1),
		results: make(chan []string, 1),
		ctx:     ctx,
		cancel:  cancel,
	}
}

func cloneInput(in Input) Input {
	in.RateLimits = maps.Clone(in.RateLimits)
	if in.ContextPercent != nil {
		pct := *in.ContextPercent
		in.ContextPercent = &pct
	}
	return in
}

// Set replaces the input of the next run and says whether a turn is
// running. Entering or leaving a turn starts the next run at once, so the
// refresh rate follows the turn without waiting out the old interval. Set
// never blocks on a run.
func (r *Runner) Set(in Input, busy bool) {
	r.mu.Lock()
	changed := busy != r.busy
	r.input = cloneInput(in)
	r.busy = busy
	r.mu.Unlock()
	if changed {
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
}

// Results returns the channel the rows of each run are delivered on. It
// holds at most one result: a result nobody read yet is replaced by the
// next one.
func (r *Runner) Results() <-chan []string {
	return r.results
}

// Run queries howmuchleft until Stop, sleeping between runs.
func (r *Runner) Run() {
	for {
		r.mu.Lock()
		in, busy := r.input, r.busy
		r.mu.Unlock()

		rows := Query(r.ctx, in)
		if r.ctx.Err() != nil {
			return
		}
		r.deliver(rows)

		interval := IdleInterval
		if busy {
			interval = BusyInterval
		}
		timer := time.NewTimer(interval)
		select {
		case <-r.ctx.Done():
			timer.Stop()
			return
		case <-r.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// deliver puts rows on the results channel, dropping a result that was
// not read yet. Run is the only sender, so the send after the drain never
// blocks.
func (r *Runner) deliver(rows []string) {
	select {
	case <-r.results:
	default:
	}
	r.results <- rows
}

// Stop ends Run and kills a run in progress. It may be called more than
// once.
func (r *Runner) Stop() {
	r.stopOnce.Do(r.cancel)
}
