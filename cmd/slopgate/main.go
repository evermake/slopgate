// Command slopgate is a local quality gate for AI coding agents.
//
// It never modifies a repository: its only output is feedback.
package main

import (
	"fmt"
	"os"
)

const usage = `slopgate — a local quality gate that gives coding agents feedback before sloppy changes reach main.

Usage:
  slopgate init             Register this repo: create its gate repo, hooks and .slopgate/ config
  slopgate daemon           Run the gate daemon in the foreground (keep this in its own terminal)
  slopgate wait [<sha>]     Block until the gate verdict for <sha> (default HEAD), then print it
  slopgate status [<sha>]   Print the current state without blocking

Exit codes for wait/status:
  0  passed
  1  failed
  2  error (daemon down, unknown commit, timeout, or still running)

Typical loop:
  slopgate daemon                  # once, in another terminal
  git commit -am "..."
  git push slopgate HEAD
  slopgate wait
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit(os.Args[2:])
	case "daemon":
		err = cmdDaemon(os.Args[2:])
	case "wait":
		err = cmdWait(os.Args[2:], true)
	case "status":
		err = cmdWait(os.Args[2:], false)
	case "hook":
		err = cmdHook(os.Args[2:])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "slopgate: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}

	if err != nil {
		// exitError carries a verdict-derived code; everything else is an
		// operational failure, which is exit 2.
		if ee, ok := err.(exitError); ok {
			if ee.msg != "" {
				fmt.Fprintln(os.Stderr, "slopgate: "+ee.msg)
			}
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, "slopgate: "+err.Error())
		os.Exit(2)
	}
}

type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }
