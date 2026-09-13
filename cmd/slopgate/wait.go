package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/evermake/slopgate/internal/config"
	"github.com/evermake/slopgate/internal/git"
	"github.com/evermake/slopgate/internal/ipc"
	"github.com/evermake/slopgate/internal/model"
	"github.com/evermake/slopgate/internal/store"
)

// cmdWait implements both `wait` (block) and `status` (poll once). They differ
// only in which RPC they call, so they share everything else.
func cmdWait(args []string, block bool) error {
	name := "status"
	if block {
		name = "wait"
	}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	timeout := fs.Duration("timeout", 30*time.Minute, "how long to block before giving up")
	asJSON := fs.Bool("json", false, "print the run record as JSON")
	// --plain is accepted now so scripts written today keep working once the
	// TUI lands and TTY auto-detection starts choosing between them.
	fs.Bool("plain", false, "force plain line-oriented output (currently the only mode)")
	fs.Parse(args)

	ctx := context.Background()
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := git.RepoRoot(ctx, cwd)
	if err != nil {
		return fmt.Errorf("not inside a git repository: %w", err)
	}
	repoID, err := config.ReadRepoID(root)
	if err != nil || repoID == "" {
		return fmt.Errorf("this repo is not registered with slopgate; run `slopgate init`")
	}

	rev := "HEAD"
	if fs.NArg() > 0 {
		rev = fs.Arg(0)
	}
	sha, err := git.RevParse(ctx, root, rev)
	if err != nil {
		return fmt.Errorf("cannot resolve %q to a commit: %w", rev, err)
	}

	st, err := store.Open("")
	if err != nil {
		return err
	}
	client, err := ipc.Dial(st.SocketPath())
	if err != nil {
		return fmt.Errorf("daemon is not running; start it with `slopgate daemon`")
	}
	defer client.Close()

	var run model.Run
	if block {
		callCtx, cancel := context.WithTimeout(ctx, *timeout+10*time.Second)
		defer cancel()
		err = client.Call(callCtx, "run.wait", map[string]any{
			"repo_id":    repoID,
			"sha":        sha,
			"timeout_ms": timeout.Milliseconds(),
		}, &run)
	} else {
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		err = client.Call(callCtx, "run.get", map[string]any{"repo_id": repoID, "sha": sha}, &run)
	}
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(run); err != nil {
			return err
		}
		return verdictExit(&run)
	}

	printRun(st, &run)
	return verdictExit(&run)
}

func printRun(st *store.Store, run *model.Run) {
	// The feedback artifact leads with its own verdict heading, so the status
	// line is printed only when there is no artifact to print.
	body, _ := st.ReadFeedback(run.RepoID, run.SHA)
	hasBody := strings.TrimSpace(body) != ""

	if !hasBody {
		fmt.Printf("slopgate: %s  %s", strings.ToUpper(string(run.State)), shortSHA(run.SHA))
		if run.Branch != "" {
			fmt.Printf(" on %s", run.Branch)
		}
		fmt.Println()
	}

	if run.State == model.StateErrored && run.Error != "" {
		fmt.Println()
		fmt.Println("slopgate could not complete this run. This is not a verdict on your code:")
		fmt.Println("  " + run.Error)
	}
	if !run.State.Terminal() {
		fmt.Println()
		fmt.Println("The run is still going. The verdict stays collectable — re-run `slopgate wait`.")
		return
	}

	if hasBody {
		fmt.Println(strings.TrimRight(body, "\n"))
		fmt.Println()
		fmt.Printf("Feedback saved at %s\n", st.FeedbackPath(run.RepoID, run.SHA))
		return
	}

	if run.State == model.StatePassed {
		fmt.Println()
		fmt.Println("No rule violations. check.sh and test.sh passed.")
	}
}

// verdictExit maps the run state onto the documented exit codes so an agent can
// branch on the result without parsing anything.
func verdictExit(run *model.Run) error {
	switch run.State {
	case model.StatePassed:
		return nil
	case model.StateFailed:
		return exitError{code: 1}
	default:
		return exitError{code: 2}
	}
}
