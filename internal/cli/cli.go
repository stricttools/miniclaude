// Package cli is miniclaude's command line: repl, the fullscreen REPL on a
// Claude Code session; mock, the same REPL on a mock session; and history
// import, which turns the Python REPL's history file into the history file
// the REPL reads. The framework provides version and help.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/stricttools/claudestream"
	"github.com/stricttools/miniclaude/internal/history"
	"github.com/stricttools/miniclaude/internal/mock"
	"github.com/stricttools/miniclaude/internal/repl"
	"github.com/stricttools/miniclaude/internal/session"
	"github.com/stricttools/strictcli/go/strictcli"
	xterm "golang.org/x/term"
)

// Exit codes the handlers return.
const (
	exitFailure = 1
	exitUsage   = 2
)

// New builds the application with every command registered.
func New(version string) *strictcli.App {
	app := strictcli.NewApp("miniclaude", version,
		"Less is more: Claude Code minus the bloatware and the bullshit")
	registerRepl(app)
	registerMock(app)
	registerHistory(app)
	return app
}

// refusal ends a command before it did anything: the message on stderr and
// the usage exit code.
func refusal(ctx *strictcli.Context, msg string) strictcli.Outcome {
	ctx.Error(msg)
	return strictcli.Exit(exitUsage)
}

// failure ends a command that failed while it ran.
func failure(ctx *strictcli.Context, err error) strictcli.Outcome {
	ctx.Error(err.Error())
	return strictcli.Exit(exitFailure)
}

// interactiveRefusal returns why command cannot run here, or "" when it
// can: the REPL draws on a terminal and has no machine output.
func interactiveRefusal(ctx *strictcli.Context, command string) string {
	if ctx.JSON() {
		return command + " is an interactive fullscreen program and has no --json output; run it without --json"
	}
	if !xterm.IsTerminal(int(os.Stdin.Fd())) {
		return command + " needs a terminal: standard input is not a terminal"
	}
	if !xterm.IsTerminal(int(os.Stdout.Fd())) {
		return command + " needs a terminal: standard output is not a terminal"
	}
	return ""
}

// untilDone returns a context cancelled when the command's context is
// cancelled (a SIGINT or SIGTERM) or when the returned stop is called.
func untilDone(ctx *strictcli.Context) (context.Context, context.CancelFunc) {
	c, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-c.Done():
		}
	}()
	return c, cancel
}

// replConfig is what the repl and mock commands share: the terminal, the
// history file, and the cancellation.
func replConfig(ctx *strictcli.Context) (repl.Config, error) {
	path, err := history.Path()
	if err != nil {
		return repl.Config{}, err
	}
	entries, err := history.Load(path)
	if err != nil {
		return repl.Config{}, err
	}
	return repl.Config{
		In:          os.Stdin,
		Out:         os.Stdout,
		HistoryPath: path,
		History:     entries,
		Cancel:      ctx.Done(),
	}, nil
}

// runRepl runs the REPL on sess, closes sess, prints the cost summary to
// the normal screen, and ends the command.
func runRepl(ctx *strictcli.Context, cfg repl.Config, sess session.Session) strictcli.Outcome {
	cfg.Session = sess
	outcome, runErr := repl.Run(cfg)
	closeErr := sess.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("closing the session: %w", closeErr)
	}
	if outcome.Summary != "" {
		ctx.Out(strings.TrimSuffix(outcome.Summary, "\n"))
	}
	if err := errors.Join(runErr, closeErr); err != nil {
		ctx.Error(err.Error())
	}
	if outcome.Signal != nil {
		n, _ := outcome.Signal.(syscall.Signal)
		if outcome.Signal == syscall.SIGHUP {
			ctx.Error("the terminal hung up (SIGHUP)")
		}
		return strictcli.Exit(128 + int(n))
	}
	if runErr != nil || closeErr != nil {
		return strictcli.Exit(exitFailure)
	}
	return strictcli.Exit(0)
}

func registerRepl(app *strictcli.App) {
	resume := strictcli.MemberChoice(
		strictcli.StringFlag("resume", "ID of the session to resume", strictcli.Required()),
		"Resume a previous session by ID")
	continueLast := strictcli.MemberChoice(
		strictcli.BoolFlag("continue-session", "Continue the most recent session in the working directory", strictcli.Required()),
		"Continue the most recent session in the working directory")
	fresh := strictcli.MemberChoice(
		strictcli.BoolFlag("new-session", "Start a fresh session", strictcli.Required()),
		"Start a fresh session")

	handler := func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		if msg := interactiveRefusal(ctx, "repl"); msg != "" {
			return refusal(ctx, msg)
		}
		resolution := strictcli.Match(strictcli.GetElected(kwargs, "session"),
			strictcli.When(resume, func(f strictcli.Fields) claudestream.SessionResolution {
				return claudestream.ResumeSession{ID: strictcli.Get[string](f, "value")}
			}),
			strictcli.When(continueLast, func(strictcli.Fields) claudestream.SessionResolution {
				return claudestream.ContinueLastSession{}
			}),
			strictcli.When(fresh, func(strictcli.Fields) claudestream.SessionResolution {
				return claudestream.NewSession{}
			}),
		)
		opts := session.Options{
			Profile:        strictcli.Get[string](kwargs, "profile"),
			Model:          strictcli.Get[string](kwargs, "model"),
			PermissionMode: strictcli.Get[string](kwargs, "permission_mode"),
			Resolution:     resolution,
		}
		if cwd, ok := strictcli.GetOpt[string](kwargs, "cwd"); ok {
			opts.Cwd = cwd
		}
		var err error
		if opts.ClaudeBinary, err = binary(kwargs, "claude_binary", claudestream.ClaudeProgram); err != nil {
			return refusal(ctx, err.Error())
		}
		if opts.ClaudewheelBinary, err = binary(kwargs, "claudewheel_binary", claudestream.ClaudewheelProgram); err != nil {
			return refusal(ctx, err.Error())
		}

		cfg, err := replConfig(ctx)
		if err != nil {
			return failure(ctx, err)
		}
		cfg.Model = opts.Model
		cfg.PermissionMode = opts.PermissionMode
		cfg.Cwd = opts.Cwd
		if cfg.Cwd == "" {
			if cfg.Cwd, err = os.Getwd(); err != nil {
				return failure(ctx, fmt.Errorf("reading the working directory: %w", err))
			}
		} else {
			if cfg.Cwd, err = filepath.Abs(cfg.Cwd); err != nil {
				return failure(ctx, fmt.Errorf("making --cwd absolute: %w", err))
			}
			opts.Cwd = cfg.Cwd
		}

		startCtx, stop := untilDone(ctx)
		sess, err := session.Start(startCtx, opts)
		stop()
		if err != nil {
			return failure(ctx, fmt.Errorf("starting Claude Code: %w", err))
		}
		return runRepl(ctx, cfg, sess)
	}

	app.Command("repl", "Start the interactive fullscreen REPL on a Claude Code session", handler,
		// mutating: spawns Claude Code under a claudewheel profile, which reads
		// and writes files under the working directory, runs shell commands,
		// calls the network, spends money, and keeps a session transcript; it
		// also appends every submitted line to the history file.
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithDryRunUnsupported("the REPL runs Claude Code as a subprocess and appends to the history file outside the effects handle, so a dry run could not preview it"),
		strictcli.WithInteractive(),
		strictcli.WithFlags(
			strictcli.StringFlag("profile", "claudewheel profile to use", strictcli.Required()),
			strictcli.StringFlag("model", "Model to use, e.g. sonnet, haiku", strictcli.Required()),
			strictcli.StringFlag("permission-mode", "Permission mode", strictcli.Required(),
				strictcli.Choices(
					strictcli.Ch("default", "ask before every edit and command"),
					strictcli.Ch("acceptEdits", "accept file edits without asking"),
					strictcli.Ch("plan", "plan only -- propose, never act"),
					strictcli.Ch("bypassPermissions", "ask for nothing at all"),
					strictcli.Ch("dontAsk", "act without asking, refusing what needs consent"),
					strictcli.Ch("auto", "let Claude Code pick the mode"),
				)),
			strictcli.StringFlag("cwd", "Working directory. Omitted, the REPL runs in the current directory.", strictcli.Optional()),
			strictcli.StringFlag("claude-binary", "Absolute path of the claude program. Omitted, claude is looked up on PATH.", strictcli.Optional()),
			strictcli.StringFlag("claudewheel-binary", "Absolute path of the claudewheel program. Omitted, claudewheel is looked up on PATH.", strictcli.Optional()),
			strictcli.MemberChoiceFlag("session", "Which session the REPL runs. Omitted, a new session starts, as with --new-session.",
				strictcli.Default("new-session"), resume, continueLast, fresh),
		),
	)
}

// binary returns the absolute path given in the flag key, or program's
// path on PATH when the flag was omitted.
func binary(kwargs map[string]interface{}, key, program string) (string, error) {
	if path, ok := strictcli.GetOpt[string](kwargs, key); ok {
		if !filepath.IsAbs(path) {
			return "", fmt.Errorf("--%s must be an absolute path, got %q", strings.ReplaceAll(key, "_", "-"), path)
		}
		return path, nil
	}
	return claudestream.ResolveOnPath(program)
}

// seedLimit bounds a seed picked at random, as the Python mock picked it.
const seedLimit = 1 << 31

func registerMock(app *strictcli.App) {
	handler := func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		if msg := interactiveRefusal(ctx, "mock"); msg != "" {
			return refusal(ctx, msg)
		}
		seed := rand.Uint64N(seedLimit)
		if raw, ok := strictcli.GetOpt[string](kwargs, "seed"); ok {
			parsed, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				return refusal(ctx, fmt.Sprintf("--seed must be a non-negative integer, got %q", raw))
			}
			seed = parsed
		}
		cfg, err := replConfig(ctx)
		if err != nil {
			return failure(ctx, err)
		}
		sess, err := mock.NewLive(seed)
		if err != nil {
			return failure(ctx, err)
		}
		cfg.Model = mock.Model
		cfg.PermissionMode = "default"
		if cfg.Cwd, err = os.Getwd(); err != nil {
			return failure(ctx, fmt.Errorf("reading the working directory: %w", err))
		}
		cfg.Intro = fmt.Sprintf("mock seed: %d", seed)
		return runRepl(ctx, cfg, sess)
	}

	app.Command("mock", "Run the interactive REPL against a mock session for TUI testing; no Claude Code needed", handler,
		// mutating: the session is fake (no Claude Code, no network, no
		// spend), but the REPL is the real one and appends every submitted
		// line to the history file.
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithDryRunUnsupported("the REPL appends to the history file outside the effects handle, so a dry run could not preview it"),
		strictcli.WithInteractive(),
		strictcli.WithFlags(
			strictcli.StringFlag("seed", "Random seed (a non-negative integer) for reproducible content. Omitted, one is picked at random; the seed is the first output line either way.", strictcli.Optional()),
		),
	)
}

// importSchema is the payload of history import.
var importSchema = strictcli.SchemaObject(
	map[string]interface{}{
		"from":        strictcli.SchemaType("string"),
		"destination": strictcli.SchemaType("string"),
		"entries":     strictcli.SchemaType("integer"),
	},
	[]string{"from", "destination", "entries"},
	false,
)

func registerHistory(app *strictcli.App) {
	group := app.Group("history", "Manage the REPL's input history file")
	handler := func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		from := strictcli.Get[string](kwargs, "from")
		dest, err := history.Path()
		if err != nil {
			return failure(ctx, err)
		}
		if _, err := os.Lstat(dest); err == nil {
			return refusal(ctx, fmt.Sprintf("%s already exists; history import only creates a new history file", dest))
		} else if !errors.Is(err, fs.ErrNotExist) {
			return failure(ctx, fmt.Errorf("checking %s: %w", dest, err))
		}
		data, err := os.ReadFile(from)
		if err != nil {
			return failure(ctx, fmt.Errorf("reading --from: %w", err))
		}
		entries := history.ParsePromptToolkit(data)
		content, err := history.Marshal(entries)
		if err != nil {
			return failure(ctx, err)
		}

		fx := ctx.Effects()
		dir := filepath.Dir(dest)
		// The directory is made private before the file is written, so the
		// file is never readable by others, whatever mode it is written with.
		if _, err := fx.Mkdir(dir); err != nil {
			return failure(ctx, err)
		}
		if _, err := fx.Chmod(dir, 0o700); err != nil {
			return failure(ctx, err)
		}
		if _, err := fx.Write(dest, content); err != nil {
			return failure(ctx, err)
		}
		if _, err := fx.Chmod(dest, 0o600); err != nil {
			return failure(ctx, err)
		}

		ctx.Payload(map[string]interface{}{"from": from, "destination": dest, "entries": len(entries)})
		verb := "imported"
		if ctx.DryRun() {
			verb = "would import"
		}
		ctx.Out(fmt.Sprintf("%s %d entries from %s into %s", verb, len(entries), from, dest))
		return strictcli.Exit(0)
	}

	group.Command("import", "Create the history file from the Python REPL's prompt_toolkit history file", handler,
		// mutating: creates the history file and its directory, through the
		// effects handle, so a dry run records the writes.
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.PayloadSchema(importSchema),
		strictcli.WithFlags(
			strictcli.StringFlag("from", "Path of the prompt_toolkit history file to read (the Python REPL kept it at ~/.miniclaude/history). The destination is the history file the REPL reads; the command refuses when it already exists.", strictcli.Required()),
		),
	)
}
