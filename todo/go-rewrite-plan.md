# Rewriting miniclaude in Go: the plan

The owner asked for miniclaude to be rewritten entirely in Go, with no decisions sent to the owner, and without running anything that uses much memory (no builds, compiles, tests, or installs; read-only and simple commands are fine). The design decisions below come from a read-only investigation, approved by the orchestrating session. miniclaude is rewritten alongside claudestream, which it is built on; claudestream's plan is in its `todo/go-rewrite-plan.md`, and its build rules (dependencies from the local module cache, `go.sum` lines copied from siblings, the strictcli v0.38.0 API read from the module cache, the Python left untouched) apply here too. Implementation decisions taken during the build go into `todo/go-rewrite-plan.deviations.md` (append-only).

## Rules for the build

- Nothing memory-heavy is run: no `go build`, `go vet`, `go get`, `go mod tidy`, tests, or installs. Each slice is checked by reading. No `_test.go` files are written.
- The Python stays untouched: `*.py`, `pyproject.toml`, `uv.lock`, `package.json`, `package-lock.json`, `.npmignore`, `bin/*.mjs`, `.rlsbl/` (changelog entries excepted), `CHANGELOG.md`, `README.md`, `CLAUDE.md`, `.strictcli/`, `tests/`, `scripts/`.

## Inventory of the Python (version 0.3.0, published to PyPI and npm)

| Module | Responsibility |
|---|---|
| `_repl.py` | the turn loop; event dispatch; slash commands; the howmuchleft status bar (run synchronously during rendering, 0.5 s timeout, cached 0.25 s during a turn and 1 s idle, stale output kept on failure); the prompt_toolkit fullscreen app (output blocks with an incremental cache, wrapped lines, scroll lock, wheel coalescing, boundary hints, a framed input of 1 to 10 lines, file history with auto-suggest, keys) |
| `_render.py` | the pure streaming markdown renderer emitting whole lines only (headers, bullets, inline code, bold, italic, links, fences, thinking shown dim gray under a "✻ thinking" header) and box-drawn tables (three-tier width fitting, word wrapping keeping SGR state) |
| `_mock.py` | the mock session: scripted mode for tests; seeded live mode with a commands table (text, thinking, tools, dialogs, status, error, slow, `md <markdown>`, demo, help) |
| `_dialogs.py` | permission decision text (Bash command; Edit diff in red and green up to 40 lines; Write preview up to 20 lines with a byte count; other tools as compact JSON up to 200 characters); choices (allow once, allow always per suggestion, deny, deny with a message); AskUserQuestion (single choice plus "Other"; multiple choice by numbers plus an optional extra entry; answers `{**input, "answers":{q: a}}`, multiple answers joined by `", "`); dialog notice and cancel |
| `_cli.py` | `version`; `repl` (`--profile`, `--model`, `--permission-mode` from default/acceptEdits/plan/bypassPermissions/dontAsk/auto, all required; `--cwd`; `--resume`, `--continue-session`, or `--new-session`, defaulting to new); `mock --seed` |
| `_toolline.py` | one-line tool-use and tool-result formatters |

Files: `~/.miniclaude/history` in prompt_toolkit's format. External programs: `howmuchleft` (no arguments; JSON on stdin with `model`, `cwd`, `cost.total_cost_usd`, `session_id`, `context_window.used_percentage`, and `rate_limits.<type>.{used_percentage, resets_at}`), and claudestream. Dead code not ported: `scripts/miniclaude-dev` and table rendering without a callback. Live callers: claudewheel's client adapter (argv `[<binary>, "repl", "--profile", P, ("--model", id)?, ("--permission-mode", one of bypassPermissions/default/plan/auto)?, ("--continue-session" | "--resume", ID)?]`), claudewheel's README, help, and demo script, and `~/Projects/stricttools/data/tools.json`.

## Go design

Module `github.com/stricttools/miniclaude`, with `main` at the root (`main.go`, `version.go`). `go.mod` requires `github.com/stricttools/claudestream v0.0.0-00010101000000-000000000000` with `replace github.com/stricttools/claudestream => ../claudestream`, so no tool ever asks the module proxy about an unpublished claudestream version; it also requires strictcli v0.38.0, `golang.org/x/term`, `golang.org/x/sys`, and `github.com/mattn/go-runewidth`.

There is no terminal framework (none is in the module cache, and its API could not be checked without compiling); the layer is written on `golang.org/x/term` and the standard library.

| Package | Responsibility |
|---|---|
| `internal/term` | raw mode with restore (also on panic); alternate screen `?1049`; hidden cursor; SGR mouse `?1000` plus `?1006`; bracketed paste `?2004`; size; SIGWINCH through `os/signal` |
| `internal/keys` | decoder for runes, Enter `\r`, Alt+Enter `\x1b\r`, a lone Escape (nothing within 50 ms), Ctrl+C/D/A/E/K/U/W, Backspace, Delete, arrows, Home and End, wheel up and down (`CSI <64` and `<65`), and paste |
| `internal/screen` | composes the output rows, the hint rule (reverse-video yellow, 0.4 s), the input frame (`┌─┐│└┘`), and the three status lines; rewrites only changed rows |
| `internal/output` | prose and table blocks; tables materialized at the live width; hard wrapping with runewidth, re-emitting SGR state on each wrapped row; follows the tail unless the user scrolled; wheel coalescing (divisor 10, burst gap 0.3 s, the first event moves at once); a downward move reaching the bottom releases the scroll lock |
| `internal/input` | multiline editing, history up and down, auto-suggest of the latest history entry with the typed prefix (Right accepts) |
| `internal/history` | JSONL `{"text": …}` at `$XDG_STATE_HOME/miniclaude/history.jsonl` (`~/.local/state` when unset) |
| `internal/render`, `internal/toolline`, `internal/dialogs` | pure, rule-by-rule ports, widths through runewidth; the renderer keeps its rule that output is identical however the stream is split into chunks |
| `internal/session` | the interface `{Send(ctx, text) (EventSource, error); Interrupt; SetModel; SetPermissionMode; GetContextUsage; RespondAllow/Deny/DialogCancelled; ModelName; PermissionMode; TotalCostUSD; TotalTokens; TurnCount; Close}` and the adapter over the claudestream session; two implementations (the adapter and the mock) justify the interface |
| `internal/mock` | the seeded port with `math/rand/v2`; its output differs from the Python mock's |
| `internal/howmuchleft` | a background goroutine with the same time limits; "howmuchleft not installed" when missing; a red failure line on error (no stale data) |
| `internal/repl` | one controller goroutine owning all state, selecting over keys, resize, a 250 ms tick, session events, status results, and modal requests |

Behavior in the controller:

- Turns run in their own goroutine with the dispatch rules ported from `_dispatch`; `PermissionDecided` is ignored.
- Permission and question flows are straight-line ports in the turn goroutine: asking for a choice or text hands a modal to the controller and blocks on an answer channel. The modal replaces the input frame; choices are picked with digits or arrows and Enter.
- Escape or Ctrl+C in a modal denies with "Denied by user", or "User dismissed the question." for AskUserQuestion.
- Prompts typed during a turn are queued and shown as "queued: …".
- Escape or Ctrl+C during a turn interrupts with a 30 s limit; past it, "interrupt got no answer in 30s; the session is unusable" is printed and the session is treated as unusable.
- Ctrl+C when idle clears the input; Ctrl+D on empty input exits.
- Slash commands: `/model`, `/mode`, `/context`, `/cost`, `/help`, `/quit`, `/exit`; unknown commands are sent as typed.
- The result line keeps the Python format; on exit the cost summary goes to the normal screen.

Commands:

| Command | Classification | Flags and behavior |
|---|---|---|
| `repl` | mutating, interactive, dry run unsupported (subprocess and history writes) | flags unchanged, so claudewheel's argv still works; the session choice becomes optional (absent means a new session, stated in help); optional `--claude-binary` and `--claudewheel-binary`; refuses when stdin or stdout is not a terminal, and refuses `--json` |
| `mock` | mutating, interactive, dry run unsupported | `--seed` optional; the seed is shown as the first output line |
| `history import` | mutating, writes through the effects handle so dry run works | `--from <path>` required; parses prompt_toolkit's format (`#` lines start an entry, `+` lines are its text); refuses when the destination exists |

Dropped: the `version` command (the framework provides it) and `scripts/miniclaude-dev`.

## Build order

Each slice is checked by reading: imports used, referenced names present, errors never dropped, a rule-by-rule comparison with each Python function, the terminal-restore paths, and no blocking work on the controller goroutine. miniclaude's slices follow claudestream's library slices.

1. Pure ports: render, toolline, dialogs builders, and the format helpers (context percentage, result line, cost line, context listing).
2. Terminal: term, keys, screen, output, input, history.
3. The session interface, the adapter, and the mock.
4. The controller and howmuchleft.
5. `main.go` and the commands.

## The switchover (separate work)

1. claudewheel provides `profile exec` (claudestream's plan describes the contract).
2. Compile, vet, and red-green suites, including pty tests for the terminal layer.
3. Retire the Python: delete the package, `pyproject.toml`, `uv.lock`, the npm shim, and the Python CI; rlsbl targets to `["go"]`; `VERSION`; rewrite the selfdoc docs; changelog entries for every behavior change against 0.3.0; PyPI and npm stay at their last versions.
4. After claudestream's release, drop the `replace` and require it; release miniclaude. Never a 1.x tag.
5. Swap the installs; run `history import` from `~/.miniclaude/history`; update `data/tools.json`; falsified-texts sweeps in claudewheel's README and help.

## Risks

- Uncompiled code: type errors and strictcli v0.38.0 API mismatches surface at the first compile.
- The hand-written terminal layer is the largest risk: Escape versus Alt timing, mouse and paste decoding, wrapped-row scroll math, and resize all need a pty harness.
- No session starts until `claudewheel profile exec` exists.
