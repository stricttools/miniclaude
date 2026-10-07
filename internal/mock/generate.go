package mock

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/stricttools/claudestream"
	"github.com/stricttools/miniclaude/internal/displaytext"
)

// Delays between streamed chunks.
const (
	chunkDelay     = 10 * time.Millisecond
	slowChunkDelay = 80 * time.Millisecond
)

// contextWindow is the model's context window in each live Result.
const contextWindow = 200_000

// errorText is the explanation of the failed turn the error command makes,
// sent as an AssistantText with no text deltas before it.
const errorText = "Your credit balance is too low to access the Anthropic API. " +
	"Please go to Plans & Billing to upgrade or purchase credits.\n"

// rateLimitEpoch is a fixed time (in 2027) the reset times count from, so
// they depend on the seed and the turn only.
const rateLimitEpoch = 1_800_000_000

// rateLimitSpec describes how one rate limit rises: from base by step per
// turn, resetting within window seconds.
type rateLimitSpec struct {
	name   string
	base   float64
	step   float64
	window uint64
}

var rateLimitSpecs = []rateLimitSpec{
	{name: "five_hour", base: 0.10, step: 0.06, window: 18_000},
	{name: "seven_day", base: 0.05, step: 0.02, window: 604_800},
}

// lorem is the filler for table cells, list items, and prose.
var lorem = []string{
	"lorem", "ipsum", "dolor", "sit", "amet", "consectetur", "adipiscing", "elit",
	"sed", "eiusmod", "tempor", "incididunt", "labore", "magna", "aliqua", "enim",
	"minim", "veniam", "quis", "nostrud", "ullamco", "laboris", "aliquip", "commodo",
	"consequat", "duis", "aute", "irure", "reprehenderit", "voluptate", "velit",
	"esse", "cillum", "fugiat", "nulla", "pariatur", "excepteur", "occaecat",
	"cupidatat", "proident", "culpa", "officia", "deserunt", "mollit", "animus",
}

// commands lists the mock commands, shown by help and after an unknown
// command.
var commands = []struct{ name, description string }{
	{"table", "markdown table with random dimensions and cell contents"},
	{"text", "multi-paragraph markdown: headers, lists, code, bold, inline code"},
	{"thinking", "a few thinking deltas followed by a short answer"},
	{"tools", "tool use/result pairs incl. an error and a subagent case"},
	{"dialogs", "a permission prompt, an AskUserQuestion, and an unsupported dialog, awaiting your answers"},
	{"status", "APIRetry, BudgetThreshold, and CompactBoundary status events"},
	{"error", "a non-streamed error explanation followed by an is_error result"},
	{"slow", "a long, slow stream for interrupt / type-ahead / resize testing"},
	{"md <markdown>", "stream the given markdown back verbatim"},
	{"demo", "run every section above in one turn"},
	{"help", "list these commands"},
}

// TextDelta is a top-level text_delta streaming event carrying text.
func TextDelta(text string) claudestream.StreamDelta {
	return claudestream.StreamDelta{Event: map[string]any{
		"type":  "content_block_delta",
		"delta": map[string]any{"type": "text_delta", "text": text},
	}}
}

// ThinkingDelta is a top-level thinking_delta streaming event carrying
// text.
func ThinkingDelta(text string) claudestream.StreamDelta {
	return claudestream.StreamDelta{Event: map[string]any{
		"type":  "content_block_delta",
		"delta": map[string]any{"type": "thinking_delta", "thinking": text},
	}}
}

// randInt returns an integer from lo to hi, both included.
func randInt(r *rand.Rand, lo, hi int) int {
	return lo + r.IntN(hi-lo+1)
}

// uniform returns a number from lo to hi.
func uniform(r *rand.Rand, lo, hi float64) float64 {
	return lo + (hi-lo)*r.Float64()
}

// splitCommand splits a prompt into its first word and the rest, as
// Python's str.split(maxsplit=1) does: leading white space is dropped from
// both, trailing white space is kept in the rest.
func splitCommand(prompt string) (string, string) {
	rest := displaytext.TrimLeftSpace(prompt)
	i := strings.IndexFunc(rest, displaytext.IsSpace)
	if i < 0 {
		return rest, ""
	}
	return rest[:i], displaytext.TrimLeftSpace(rest[i:])
}

// generator makes up one live turn's events in its own goroutine. Every
// method that emits returns false once the turn is stopped, and its caller
// then returns false at once.
type generator struct {
	s *Session
	t *liveTurn
}

func (g *generator) run(prompt string, systemInit *claudestream.SystemInit) {
	defer close(g.t.events)
	start := time.Now()
	if systemInit != nil && !g.emit(*systemInit) {
		return
	}
	cmd, arg := splitCommand(prompt)
	if !g.dispatch(cmd, arg) {
		return
	}
	if !g.s.errorPending {
		for _, ev := range g.rateLimitEvents() {
			if !g.emit(ev) {
				return
			}
		}
	}
	g.emit(g.result(start))
}

// dispatch runs the command named by cmd; the names are case-sensitive.
func (g *generator) dispatch(cmd, arg string) bool {
	switch cmd {
	case "table":
		return g.streamText(g.table(), chunkDelay)
	case "text":
		return g.streamText(g.textBody(), chunkDelay)
	case "thinking":
		return g.thinking()
	case "tools":
		return g.tools()
	case "dialogs":
		return g.dialogs()
	case "status":
		return g.status()
	case "error":
		return g.failure()
	case "slow":
		return g.slow(randInt(g.s.rng, 15, 25))
	case "md":
		return g.streamText(arg, chunkDelay)
	case "demo":
		return g.demo()
	case "help":
		return g.streamText(commandList(), chunkDelay)
	}
	return g.streamText("Unknown command: `"+cmd+"`\n\n"+commandList(), chunkDelay)
}

func (g *generator) emit(ev claudestream.Event) bool {
	select {
	case g.t.events <- ev:
		return true
	case <-g.t.stop:
		return false
	}
}

func (g *generator) sleep(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-g.t.stop:
		return false
	}
}

// streamText emits text as text deltas of 8 to 32 characters, pausing delay
// after each. An interrupt ends the stream early.
func (g *generator) streamText(text string, delay time.Duration) bool {
	runes := []rune(text)
	for i := 0; i < len(runes); {
		if g.s.interrupted.Load() {
			return true
		}
		end := min(i+randInt(g.s.rng, 8, 32), len(runes))
		if !g.emit(TextDelta(string(runes[i:end]))) {
			return false
		}
		i = end
		if !g.sleep(delay) {
			return false
		}
	}
	return true
}

// ask emits a request and waits for its answer.
func (g *generator) ask(ev claudestream.Event, requestID string, dialog bool) (answer, bool) {
	p := &pendingRequest{id: requestID, dialog: dialog, reply: make(chan answer, 1)}
	g.s.mu.Lock()
	g.s.pending = p
	g.s.mu.Unlock()
	if !g.emit(ev) {
		return answer{}, false
	}
	select {
	case a := <-p.reply:
		return a, true
	case <-g.t.stop:
		return answer{}, false
	}
}

// rateLimitEvents are the two rate limits sent before each Result. Their
// use rises with the turn count, the five-hour limit faster, plus a small
// jitter from the rate-limit generator; past 0.8 the status is
// "allowed_warning".
func (g *generator) rateLimitEvents() []claudestream.Event {
	turn := g.s.TurnCount()
	events := make([]claudestream.Event, 0, len(rateLimitSpecs))
	for _, spec := range rateLimitSpecs {
		jitter := uniform(g.s.rateLimit, -0.01, 0.01)
		utilization := max(0.0, min(0.99, spec.base+float64(turn)*spec.step+jitter))
		status := "allowed"
		if utilization >= 0.8 {
			status = "allowed_warning"
		}
		resetsAt := int64(rateLimitEpoch) + int64(g.s.seed%spec.window) + int64(turn)*300
		events = append(events, claudestream.RateLimit{
			Status:        status,
			ResetsAt:      &resetsAt,
			RateLimitType: spec.name,
			Utilization:   utilization,
		})
	}
	return events
}

// result makes up the turn's Result and adds to the session's totals. After
// the error command the Result is an error with no added cost, carrying
// the explanation.
func (g *generator) result(start time.Time) claudestream.Result {
	s := g.s
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnCount++
	durationMS := float64(time.Since(start).Microseconds()) / 1000
	if s.errorPending {
		s.errorPending = false
		return claudestream.Result{
			Subtype:      "error",
			IsError:      true,
			Result:       strings.TrimSpace(errorText),
			TotalCostUSD: s.costUSD,
			DurationMS:   durationMS,
			NumTurns:     s.turnCount,
		}
	}
	s.costUSD = math.Round((s.costUSD+uniform(s.rng, 0.001, 0.01))*1e6) / 1e6
	s.tokens += int64(randInt(s.rng, 200, 2000))
	s.contextTokens += int64(randInt(s.rng, 1500, 6000))
	return claudestream.Result{
		Subtype:      "success",
		TotalCostUSD: s.costUSD,
		DurationMS:   durationMS,
		NumTurns:     s.turnCount,
		ModelUsage: map[string]claudestream.ModelUsage{
			Model: {InputTokens: s.contextTokens, ContextWindow: contextWindow},
		},
	}
}

func (g *generator) word() string {
	return lorem[g.s.rng.IntN(len(lorem))]
}

func (g *generator) words(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = g.word()
	}
	return strings.Join(parts, " ")
}

// cell is one table cell: a word, or a sentence of 4 to 12 words.
func (g *generator) cell() string {
	if g.s.rng.Float64() < 0.5 {
		return g.word()
	}
	return g.words(randInt(g.s.rng, 4, 12))
}

// table is a markdown table of 5 to 10 columns and 5 to 10 rows.
func (g *generator) table() string {
	cols := randInt(g.s.rng, 5, 10)
	rows := randInt(g.s.rng, 5, 10)
	row := func(cell func() string) string {
		cells := make([]string, cols)
		for i := range cells {
			cells[i] = cell()
		}
		return "| " + strings.Join(cells, " | ") + " |"
	}
	lines := []string{
		row(g.word),
		row(func() string { return "---" }),
	}
	for range rows {
		lines = append(lines, row(g.cell))
	}
	return strings.Join(lines, "\n") + "\n"
}

func (g *generator) textBody() string {
	w := g.word
	title := w()
	return fmt.Sprintf("# %s %s\n\n", strings.ToUpper(title[:1])+title[1:], w()) +
		fmt.Sprintf("A paragraph with **%s %s** and inline `code_%s` spans.\n\n", w(), w(), w()) +
		fmt.Sprintf("- %s %s\n- %s %s\n- %s %s\n\n", w(), w(), w(), w(), w(), w()) +
		fmt.Sprintf("1. %s %s\n2. %s %s\n3. %s %s\n\n", w(), w(), w(), w(), w(), w()) +
		fmt.Sprintf("```python\ndef %s():\n    return \"%s\"\n```\n", w(), w())
}

func commandList() string {
	var b strings.Builder
	b.WriteString("## Mock commands\n\n")
	for _, c := range commands {
		fmt.Fprintf(&b, "- `%s` -- %s\n", c.name, c.description)
	}
	return b.String()
}

// ackText acknowledges the answer to a request.
func ackText(a answer) string {
	switch a.action {
	case answerAllow:
		if answers, ok := a.input["answers"]; ok {
			return "You answered: " + displaytext.String(answers) + "\n"
		}
		return "Approved -- running the command.\n"
	case answerDeny:
		return "Denied: " + a.message + "\n"
	}
	return "Dialog cancelled.\n"
}

func (g *generator) thinking() bool {
	for range randInt(g.s.rng, 3, 5) {
		if !g.emit(ThinkingDelta(g.words(randInt(g.s.rng, 6, 12)) + "\n")) {
			return false
		}
		if !g.sleep(chunkDelay) {
			return false
		}
	}
	return g.streamText("Done thinking -- here is a short answer.\n", chunkDelay)
}

func (g *generator) tools() bool {
	parent := "mock-parent"
	return g.emit(claudestream.ToolUse{
		ToolUseID: "mock-t1",
		Name:      "Read",
		Input:     map[string]any{"file_path": "/mock/example.py"},
	}) &&
		g.emit(claudestream.ToolResult{
			ToolUseID: "mock-t1",
			Content:   json.RawMessage(`"file read ok"`),
			ToolName:  "Read",
		}) &&
		g.streamText("Read succeeded; running a command.\n", chunkDelay) &&
		g.emit(claudestream.ToolUse{
			ToolUseID: "mock-t2",
			Name:      "Bash",
			Input:     map[string]any{"command": "false"},
		}) &&
		g.emit(claudestream.ToolResult{
			ToolUseID: "mock-t2",
			Content:   json.RawMessage(`"command failed"`),
			ToolName:  "Bash",
			IsError:   true,
		}) &&
		g.streamText("That failed; delegating to a subagent.\n", chunkDelay) &&
		g.emit(claudestream.ToolUse{
			ToolUseID:       "mock-t3",
			Name:            "Task",
			Input:           map[string]any{"description": "sub task", "prompt": "do sub work"},
			ParentToolUseID: &parent,
		}) &&
		g.emit(claudestream.ToolResult{
			ToolUseID:       "mock-t3",
			Content:         json.RawMessage(`"subagent done"`),
			ParentToolUseID: &parent,
			ToolName:        "Task",
		})
}

func (g *generator) status() bool {
	return g.streamText("Retrying the API call.\n", chunkDelay) &&
		g.emit(claudestream.APIRetry{Attempt: 2, MaxRetries: 10}) &&
		g.streamText("Crossing a budget threshold.\n", chunkDelay) &&
		g.emit(claudestream.BudgetThreshold{
			Metric:       claudestream.BudgetMetricCost,
			Threshold:    1.0,
			CurrentValue: 1.25,
		}) &&
		g.streamText("Compacting the conversation.\n", chunkDelay) &&
		g.emit(claudestream.CompactBoundary{}) &&
		g.streamText("Recovered; continuing.\n", chunkDelay)
}

// failure is a failed turn: the explanation as an AssistantText with no
// text deltas, and an error Result.
func (g *generator) failure() bool {
	g.s.errorPending = true
	return g.emit(claudestream.AssistantText{Text: errorText})
}

// slow streams lines numbered lines of 8 to 16 words, slowly.
func (g *generator) slow(lines int) bool {
	paragraphs := make([]string, lines)
	for i := range paragraphs {
		paragraphs[i] = fmt.Sprintf("%d. %s", i+1, g.words(randInt(g.s.rng, 8, 16)))
	}
	return g.streamText(strings.Join(paragraphs, "\n")+"\n", slowChunkDelay)
}

// dialogs asks for a Bash permission, then asks an AskUserQuestion (which
// arrives as a permission request), then opens a dialog miniclaude does not
// support; each waits for its answer, which is acknowledged in text.
func (g *generator) dialogs() bool {
	permission := claudestream.PermissionRequest{
		RequestID: "mock-perm-1",
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "echo mock"},
		ToolUseID: "mock-t4",
		PermissionSuggestions: []map[string]any{{
			"type":        "addRules",
			"rules":       []any{map[string]any{"toolName": "Bash", "ruleContent": "echo mock"}},
			"behavior":    "allow",
			"destination": "session",
		}},
	}
	a, ok := g.ask(permission, permission.RequestID, false)
	if !ok || !g.streamText(ackText(a), chunkDelay) {
		return false
	}

	question := claudestream.PermissionRequest{
		RequestID: "mock-ask-1",
		ToolName:  "AskUserQuestion",
		ToolInput: map[string]any{
			"questions": []any{map[string]any{
				"question": "Which color do you prefer?",
				"options": []any{
					map[string]any{"label": "Red"},
					map[string]any{"label": "Blue"},
					map[string]any{"label": "Green"},
				},
				"multiSelect": false,
			}},
		},
		ToolUseID:             "mock-t5",
		PermissionSuggestions: []map[string]any{},
	}
	a, ok = g.ask(question, question.RequestID, false)
	if !ok || !g.streamText(ackText(a), chunkDelay) {
		return false
	}

	dialog := claudestream.UserDialogRequest{
		RequestID: "mock-dialog-1",
		Dialog:    "mock_dialog",
		Payload:   map[string]any{},
	}
	a, ok = g.ask(dialog, dialog.RequestID, true)
	return ok && g.streamText(ackText(a), chunkDelay)
}

// demo runs every section, the error last so the turn ends on its red
// result line.
func (g *generator) demo() bool {
	sections := []struct {
		title string
		run   func() bool
	}{
		{"Text", func() bool { return g.streamText(g.textBody(), chunkDelay) }},
		{"Thinking", g.thinking},
		{"Table", func() bool { return g.streamText(g.table(), chunkDelay) }},
		{"Tools", g.tools},
		{"Status", g.status},
		{"Dialogs", g.dialogs},
		{"Slow", func() bool { return g.slow(8) }},
		{"Error", g.failure},
	}
	for _, section := range sections {
		if !g.streamText("\n## "+section.title+"\n\n", chunkDelay) || !section.run() {
			return false
		}
	}
	return true
}
