# miniclaude

Less is more: Claude Code minus the bloatware and the bullshit.

It is for developers who want a minimal alternative frontend to the official Claude Code TUI. It replaces only the presentation layer: tools, permissions, sessions, and authentication all come from the `claude` CLI, started through a claudewheel profile and driven through the [claudestream](https://github.com/stricttools/claudestream) Go library.

## Design stance

- **Fullscreen, block-backed rendering.** Output is held as blocks and drawn into a scrollable region on the alternate screen. Scroll with the mouse wheel; the view holds where you leave it (scroll lock) and re-flows tables to the live terminal width on resize.
- **Line-grain streaming markdown.** Assistant prose is styled and emitted line by line as it arrives (headers, bullets, inline code, links, code fences). Tables are the one buffered exception, held until complete and drawn with aligned columns.
- **Dense tool activity.** Each tool call is one line (`▸ ToolName arg`); each result is one dim line (`✓`/`✗` plus a `(+N lines)` count). Subagent activity is indented.
- **Interactive tools that work in the terminal.** Permission prompts show a decision surface (the Bash command, a colored diff for edits, a preview for writes) above numbered Allow/Deny choices. AskUserQuestion is answered through numbered menus (single and multiple choice).
- **Type-ahead with interrupt.** Type while a turn runs and the line is queued; press Esc to interrupt the turn in flight.
- **Status rows from howmuchleft.** When `howmuchleft` is on PATH, its output is shown below the input box.

## Requirements

- The `claude` CLI installed and logged in
- [claudewheel](https://github.com/stricttools/claudewheel) and a profile; sessions start through `claudewheel profile exec`
- Optionally, `howmuchleft` for the status rows

## Install

```
go install github.com/stricttools/miniclaude@v0
```

Each GitHub Release also carries `miniclaude` archives for Linux and macOS (amd64 and arm64). The Python package on PyPI and the npm package stop at 0.3.0.

## Usage

```
miniclaude repl --profile <name> --model <model> --permission-mode <mode>
```

Example:

```
miniclaude repl --profile default --model sonnet --permission-mode default
```

| Flag | Presence | Description |
| --- | --- | --- |
| `--profile <str>` | required | claudewheel profile to use |
| `--model <str>` | required | model to use, e.g. `sonnet`, `haiku` |
| `--permission-mode <str>` | required | one of `default`, `acceptEdits`, `plan`, `bypassPermissions`, `dontAsk`, `auto` |
| `--cwd <str>` | optional | working directory; omitted, the REPL runs in the current directory |
| `--claude-binary <path>` | optional | absolute path of the `claude` program; omitted, it is looked up on PATH |
| `--claudewheel-binary <path>` | optional | absolute path of the `claudewheel` program; omitted, it is looked up on PATH |

Which session the REPL runs is one selection over three named alternatives; pass one, or none and get a new session:

| Flag | Effect |
| --- | --- |
| `--resume <session-id>` | resume that previous session |
| `--continue-session` | continue the most recent session in the working directory |
| `--new-session` | start a fresh session (what you get by passing none of the three) |

Passing two of them is refused by name. `repl` and `mock` need a terminal on standard input and output, and refuse `--json`.

Other commands:

| Command | Effect |
| --- | --- |
| `miniclaude mock [--seed <n>]` | run the REPL against a mock session, no Claude Code needed; the seed is the first output line |
| `miniclaude history import --from <path>` | create the history file from the Python REPL's prompt_toolkit history (`~/.miniclaude/history`); refused when the history file exists |

Run `miniclaude --help` for every command and flag.

## In-session

Slash commands:

| Command | Effect |
| --- | --- |
| `/model <name>` | switch model (no argument: show current) |
| `/mode <mode>` | change permission mode (no argument: show current) |
| `/context` | show context-window usage |
| `/cost` | show session cost and token totals |
| `/help` | list the slash commands |
| `/quit`, `/exit` | leave the REPL |

Unknown `/` commands are sent to the session as typed, so server-side commands still work.

Keybindings:

| Key | Action |
| --- | --- |
| Enter | submit the prompt |
| Alt+Enter | insert a newline |
| Up, Down | move between lines, and recall history from the first or last line |
| Right, Ctrl+E | accept the gray suggestion from history at the end of the input |
| Esc | interrupt the running turn; deny a permission prompt or dismiss a question |
| Ctrl+C | clear the input when idle; interrupt the running turn |
| Ctrl+D | exit (on an empty input) |

Submitted prompts are kept in `$XDG_STATE_HOME/miniclaude/history.jsonl` (`~/.local/state/miniclaude/history.jsonl` when it is unset).

## claudewheel integration

Launch it from claudewheel with `claudewheel --client miniclaude`.

## License

MIT
