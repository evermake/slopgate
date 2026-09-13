package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/evermake/slopgate/internal/git"
	"github.com/evermake/slopgate/internal/ipc"
	"github.com/evermake/slopgate/internal/store"
)

// pingTimeout is short on purpose: pre-receive runs inside `git push`, and a
// developer waiting on a hung liveness check would rather be told no.
const pingTimeout = 3 * time.Second

func cmdHook(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("hook: expected pre-receive or post-receive")
	}
	fs := flag.NewFlagSet("hook", flag.ExitOnError)
	repoID := fs.String("repo-id", "", "repo id this gate repo belongs to")
	fs.Parse(args[1:])

	st, err := store.Open("")
	if err != nil {
		return err
	}

	switch args[0] {
	case "pre-receive":
		return hookPreReceive(st)
	case "post-receive":
		return hookPostReceive(st, *repoID)
	default:
		return fmt.Errorf("hook: unknown hook %q", args[0])
	}
}

// hookPreReceive rejects the push when the daemon is down.
//
// Rejection has to happen here: post-receive runs after the refs are already
// updated and git ignores its exit code. Accepting a push nobody will gate
// would silently break the whole loop -- the agent would wait forever on a
// verdict that is never coming.
func hookPreReceive(st *store.Store) error {
	if err := ipc.Ping(st.SocketPath(), pingTimeout); err != nil {
		fmt.Fprintln(os.Stderr, "slopgate: daemon is not running, so this push cannot be gated")
		fmt.Fprintln(os.Stderr, "slopgate:   start it with:  slopgate daemon")
		return exitError{code: 1}
	}
	return nil
}

// hookPostReceive registers each pushed branch synchronously, then tells the
// pusher what to run next.
//
// Registration cannot move into pre-receive: since git 2.11 the pushed objects
// sit in a quarantine directory until pre-receive succeeds, so the daemon --
// a separate process -- cannot yet read the commit it is being asked to gate.
func hookPostReceive(st *store.Store, repoID string) error {
	if repoID == "" {
		return fmt.Errorf("hook: --repo-id is required; re-run `slopgate init`")
	}

	client, err := ipc.Dial(st.SocketPath())
	if err != nil {
		// The daemon passed pre-receive and died in between. The push has
		// already landed, so all we can do is say so.
		fmt.Fprintln(os.Stderr, "slopgate: daemon went away before the run could be registered; push again once it is back")
		return nil
	}
	defer client.Close()

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		newSHA, ref := fields[1], fields[2]
		if git.IsZeroSHA(newSHA) {
			continue // branch deletion: nothing to gate
		}
		if !strings.HasPrefix(ref, "refs/heads/") {
			continue // tags and notes are not gated
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		var res struct {
			RunID string `json:"run_id"`
		}
		err := client.Call(ctx, "run.submit", map[string]string{
			"repo_id": repoID,
			"ref":     ref,
			"sha":     newSHA,
		}, &res)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "slopgate: could not register a gate run for %s: %v\n", shortSHA(newSHA), err)
			continue
		}

		// Printed to the pusher's terminal: the agent's next action arrives in
		// the output it just received, with no harness-specific wiring.
		fmt.Fprintf(os.Stderr, "slopgate: gate started for %s\n", shortSHA(newSHA))
		fmt.Fprintf(os.Stderr, "slopgate: run `slopgate wait %s` for the verdict\n", shortSHA(newSHA))
	}
	return sc.Err()
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
