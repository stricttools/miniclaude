package repl

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/stricttools/claudestream"
	"github.com/stricttools/miniclaude/internal/dialogs"
	"github.com/stricttools/miniclaude/internal/displaytext"
	"github.com/stricttools/miniclaude/internal/howmuchleft"
	"github.com/stricttools/miniclaude/internal/render"
	"github.com/stricttools/miniclaude/internal/session"
	"github.com/stricttools/miniclaude/internal/toolline"
	"github.com/stricttools/miniclaude/internal/usageformat"
)

// errUnusable refuses a prompt after an interrupt got no answer.
var errUnusable = errors.New("the session is unusable after an interrupt got no answer; leave with /quit or Ctrl+D")

// echo is the first line of the prompt, cut to 100 characters, with an
// ellipsis when anything was left out.
func echo(prompt string) string {
	lines := displaytext.SplitLines(displaytext.TrimSpace(prompt))
	if len(lines) == 0 {
		return ""
	}
	first := []rune(lines[0])
	cut := len(first) > 100
	if cut {
		first = first[:100]
	}
	if cut || len(lines) > 1 {
		return string(first) + "…"
	}
	return string(first)
}

// startTurn sends prompt to the session on a goroutine of its own, which
// reads the turn's events and posts what they show to the controller.
func (c *controller) startTurn(prompt string) {
	if c.unusable {
		c.out.Print(errorLine(errUnusable))
		return
	}
	c.busy, c.turnActive, c.interruptPending = true, true, false
	c.turnSeq++
	seq := c.turnSeq
	ctx, cancel := context.WithCancel(c.base)
	c.turnCancel = cancel
	c.out.Print(dim("> "+echo(prompt)) + "\n")
	t := &turn{
		ctx:     ctx,
		sess:    c.sess,
		updates: c.updates,
		stopped: c.stopped,
		r:       render.New(),
	}
	c.goGuarded(func() {
		t.run(prompt)
		t.post(func(c *controller) { c.turnEnded(seq) })
	})
}

// turnEnded runs when the goroutine of turn seq is done.
func (c *controller) turnEnded(seq int) {
	if seq != c.turnSeq {
		return
	}
	c.turnCancel()
	c.busy, c.turnActive, c.interruptPending = false, false, false
	c.next()
}

// turn is one turn's goroutine state. It reads the session and posts what
// the controller should show or change; it never touches the controller's
// fields itself.
type turn struct {
	ctx     context.Context
	sess    session.Session
	updates chan<- func(*controller)
	stopped <-chan struct{}
	r       *render.Renderer

	// textStreamed and thinkingStreamed record whether top-level text or
	// thinking deltas were shown since the last whole AssistantText or
	// Thinking event; when they were, that event repeats them and is
	// dropped, and when they were not (a failed turn explains itself in an
	// AssistantText with no deltas), the event is shown.
	textStreamed     bool
	thinkingStreamed bool
}

func (t *turn) post(f func(*controller)) bool {
	return post(t.updates, t.stopped, f)
}

func (t *turn) print(text string) {
	if text == "" {
		return
	}
	t.post(func(c *controller) { c.out.Print(text) })
}

// printError shows err, unless the turn was cancelled on purpose (the REPL
// ending, or an unanswered interrupt), which the controller reports itself.
func (t *turn) printError(err error) {
	if t.ctx.Err() != nil {
		return
	}
	t.print(errorLine(err))
}

// segments shows rendered output: prose as text, tables as table blocks
// that re-render at the output's width.
func (t *turn) segments(segs []render.Segment) {
	if len(segs) == 0 {
		return
	}
	t.post(func(c *controller) {
		for _, s := range segs {
			if s.Table != nil {
				c.out.AddTable(s.Table.Render)
			} else {
				c.out.Print(s.Text)
			}
		}
	})
}

// run sends prompt and reads the turn to its end. Errors while showing an
// event are reported and reading goes on, since a turn left unread blocks
// every later one.
func (t *turn) run(prompt string) {
	src, err := t.sess.Send(t.ctx, prompt)
	if err != nil {
		t.printError(err)
		t.segments(t.r.Finish())
		return
	}
	for {
		ev, err := src.Next(t.ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.printError(err)
			break
		}
		t.dispatch(ev)
	}
	t.segments(t.r.Finish())
}

func isSubagent(parent *string) bool {
	return parent != nil
}

// dispatch shows one event.
func (t *turn) dispatch(event claudestream.Event) {
	switch ev := event.(type) {
	case claudestream.StreamDelta:
		if isSubagent(ev.ParentToolUseID) {
			return
		}
		switch ev.DeltaType() {
		case "text_delta":
			t.textStreamed = true
			t.segments(t.r.FeedText(ev.Text()))
		case "thinking_delta":
			t.thinkingStreamed = true
			t.segments(t.r.FeedThinking(ev.ThinkingText()))
		}

	case claudestream.AssistantText:
		if isSubagent(ev.ParentToolUseID) {
			return
		}
		if t.textStreamed {
			t.textStreamed = false
			return
		}
		t.segments(t.r.FeedText(ev.Text))
		t.segments(t.r.Finish())

	case claudestream.Thinking:
		if isSubagent(ev.ParentToolUseID) {
			return
		}
		if t.thinkingStreamed {
			t.thinkingStreamed = false
			return
		}
		t.segments(t.r.FeedThinking(ev.Text))
		t.segments(t.r.Finish())

	case claudestream.ToolUse:
		t.segments(t.r.Finish())
		t.print(toolline.FormatToolUse(ev.Name, ev.Input, isSubagent(ev.ParentToolUseID)) + "\n")

	case claudestream.ToolResult:
		line, err := toolline.FormatToolResult(ev.Content, ev.IsError, isSubagent(ev.ParentToolUseID))
		if err != nil {
			t.printError(err)
			return
		}
		t.print(line + "\n")

	case claudestream.PermissionRequest:
		if ev.ToolName == "AskUserQuestion" {
			t.questionFlow(ev)
		} else {
			t.permissionFlow(ev)
		}

	case claudestream.UserDialogRequest:
		t.print(dialogs.DialogNotice(ev.Dialog))
		if err := t.sess.RespondDialogCancelled(t.ctx, ev.RequestID); err != nil {
			t.printError(fmt.Errorf("cancelling dialog %q: %w", ev.Dialog, err))
		}

	case claudestream.Result:
		t.segments(t.r.Finish())
		model := t.sess.ModelName()
		usage := modelUsage(ev.ModelUsage)
		line := usageformat.ResultLine(usageformat.Result{
			TotalCostUSD: ev.TotalCostUSD,
			DurationMS:   ev.DurationMS,
			NumTurns:     ev.NumTurns,
			IsError:      ev.IsError,
			ModelUsage:   usage,
		}, model)
		cost := t.sess.TotalCostUSD()
		pct, known := usageformat.ContextPercent(usage, model)
		t.post(func(c *controller) {
			c.interruptPending = false
			c.out.Print(line)
			c.costUSD = cost
			if known {
				p := float64(pct)
				c.ctxPct = &p
			}
		})

	case claudestream.APIRetry:
		t.print(dim(fmt.Sprintf("retry %d/%d…", ev.Attempt, ev.MaxRetries)) + "\n")

	case claudestream.RateLimit:
		// No output: howmuchleft shows the limits. The limit's type is
		// passed through as the key; a later event of a type replaces it.
		limit := howmuchleft.RateLimit{UsedPercentage: ev.Utilization * 100, ResetsAt: ev.ResetsAt}
		name := ev.RateLimitType
		t.post(func(c *controller) {
			if c.rateLimits == nil {
				c.rateLimits = map[string]howmuchleft.RateLimit{}
			}
			c.rateLimits[name] = limit
		})

	case claudestream.BudgetThreshold:
		t.print(yellow(fmt.Sprintf("budget: %s crossed %s (now %s)",
			ev.Metric, displaytext.String(ev.Threshold), displaytext.String(ev.CurrentValue))) + "\n")

	case claudestream.CompactBoundary:
		t.print(dim("── compacted ──") + "\n")

	case claudestream.SystemInit:
		cwd, sessionID := ev.Cwd, ev.SessionID
		t.post(func(c *controller) {
			if cwd != "" {
				c.cwd = cwd
			}
			c.sessionID = sessionID
		})

	default:
		// FileWrite and FileEdit repeat what the ToolUse line showed;
		// PermissionDecided only follows a sandbox's own decision, and
		// every other event shows nothing.
	}
}

// modelUsage converts a result's per-model usage for usageformat.
func modelUsage(in map[string]claudestream.ModelUsage) map[string]usageformat.ModelUsage {
	out := make(map[string]usageformat.ModelUsage, len(in))
	for name, u := range in {
		out[name] = usageformat.ModelUsage{
			InputTokens:              u.InputTokens,
			CacheReadInputTokens:     u.CacheReadInputTokens,
			CacheCreationInputTokens: u.CacheCreationInputTokens,
			ContextWindow:            u.ContextWindow,
		}
	}
	return out
}

// ask shows m and waits for the answer. It returns the context's error
// when the turn is cancelled first, and closes the modal then.
func (t *turn) ask(m *modal) (modalAnswer, error) {
	if !t.post(func(c *controller) { c.openModal(m) }) {
		return modalAnswer{}, context.Canceled
	}
	select {
	case a := <-m.answer:
		return a, nil
	case <-t.ctx.Done():
		t.post(func(c *controller) { c.closeModal(m) })
		return modalAnswer{}, t.ctx.Err()
	}
}

// respond sends a decision on a permission request.
func (t *turn) respond(requestID string, d dialogs.Decision) {
	var err error
	if d.Allow {
		err = t.sess.RespondAllow(t.ctx, requestID, d.UpdatedInput, d.UpdatedPermissions)
	} else {
		err = t.sess.RespondDeny(t.ctx, requestID, d.Message)
	}
	if err != nil {
		t.printError(fmt.Errorf("answering the permission request: %w", err))
	}
}

// permissionFlow shows what the tool wants to do, asks the user to allow
// or deny it, and answers. Dismissing the menu, or the denial message's
// prompt, denies with "Denied by user".
func (t *turn) permissionFlow(ev claudestream.PermissionRequest) {
	req := dialogs.Request{
		ToolName:    ev.ToolName,
		Input:       ev.ToolInput,
		Title:       ev.Title,
		Suggestions: ev.PermissionSuggestions,
	}
	description, err := dialogs.Description(req)
	if err != nil {
		t.unreadable(ev.RequestID, err)
		return
	}
	choices, err := dialogs.PermissionChoices(req)
	if err != nil {
		t.unreadable(ev.RequestID, err)
		return
	}
	t.print(description)
	labels := make([]string, len(choices))
	for i, ch := range choices {
		labels[i] = ch.Label
	}
	picked, err := t.ask(newChoiceModal(dialogs.ChoicePrompt, labels))
	if err != nil {
		return
	}
	if picked.dismissed {
		t.respond(ev.RequestID, dialogs.PermissionDismissed())
		return
	}
	choice := choices[picked.choice]
	message := ""
	if choice.Action == dialogs.DenyWithMessage {
		typed, err := t.ask(newTextModal(dialogs.DenialMessagePrompt))
		if err != nil {
			return
		}
		if !typed.dismissed {
			message = typed.text
		}
	}
	t.respond(ev.RequestID, dialogs.Decide(req, choice, message))
}

// unreadable reports a request whose input cannot be shown and denies it,
// telling the model why, so the turn goes on.
func (t *turn) unreadable(requestID string, err error) {
	t.printError(err)
	t.respond(requestID, dialogs.Decision{Message: "miniclaude could not show this request: " + err.Error()})
}

// questionFlow asks each AskUserQuestion question in turn and answers with
// the answers. Dismissing any prompt dismisses the whole request.
func (t *turn) questionFlow(ev claudestream.PermissionRequest) {
	questions, err := dialogs.Questions(ev.ToolInput)
	if err != nil {
		t.unreadable(ev.RequestID, err)
		return
	}
	answers := make([][]string, 0, len(questions))
	for _, q := range questions {
		t.print(dialogs.QuestionHeader(q))
		answer, dismissed, err := t.askQuestion(q)
		if err != nil {
			return
		}
		if dismissed {
			t.respond(ev.RequestID, dialogs.QuestionDismissed())
			return
		}
		answers = append(answers, answer)
	}
	t.respond(ev.RequestID, dialogs.QuestionAnswers(ev.ToolInput, questions, answers))
}

// askQuestion asks one question: a multiple-choice question by option
// numbers (asked again until they parse) and an optional answer of the
// user's own, a single-choice question by a menu of its options and
// "Other", which asks for the answer as text.
func (t *turn) askQuestion(q dialogs.Question) (answer []string, dismissed bool, err error) {
	if q.MultiSelect {
		for {
			t.print(dialogs.NumberedOptions(q))
			typed, err := t.ask(newTextModal(dialogs.MultiSelectPrompt))
			if err != nil || typed.dismissed {
				return nil, typed.dismissed, err
			}
			idxs, ok := dialogs.ParseMultiSelect(typed.text, len(q.Options))
			if !ok {
				t.print(dialogs.InvalidSelection)
				continue
			}
			extra, err := t.ask(newTextModal(dialogs.ExtraAnswerPrompt))
			if err != nil || extra.dismissed {
				return nil, extra.dismissed, err
			}
			return dialogs.MultiSelectAnswer(q, idxs, extra.text), false, nil
		}
	}
	picked, err := t.ask(newChoiceModal("", dialogs.SingleChoiceMenu(q)))
	if err != nil || picked.dismissed {
		return nil, picked.dismissed, err
	}
	own := ""
	if picked.choice == len(q.Options) {
		typed, err := t.ask(newTextModal(dialogs.OwnAnswerPrompt))
		if err != nil || typed.dismissed {
			return nil, typed.dismissed, err
		}
		own = typed.text
	}
	return dialogs.SingleChoiceAnswer(q, picked.choice, own), false, nil
}
