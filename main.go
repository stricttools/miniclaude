// Command miniclaude is a lean fullscreen terminal client for Claude Code:
// a REPL with a scrollable output region, a boxed input editor, and
// howmuchleft's status rows, built on claudestream. Its commands are
// registered in internal/cli.
package main

import "github.com/stricttools/miniclaude/internal/cli"

func main() {
	cli.New(Version).Run()
}
