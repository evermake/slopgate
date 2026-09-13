package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/evermake/slopgate/internal/checker"
	"github.com/evermake/slopgate/internal/daemon"
	"github.com/evermake/slopgate/internal/runner"
	"github.com/evermake/slopgate/internal/store"
)

func cmdDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	model := fs.String("model", "", "model alias passed to the rule-check agent (default: the claude CLI's own default)")
	fs.Parse(args)

	st, err := store.Open("")
	if err != nil {
		return err
	}

	logf := func(format string, a ...any) {
		fmt.Fprintf(os.Stdout, format+"\n", a...)
	}

	ck := checker.New()
	ck.Model = *model
	// Test seam: the end-to-end tests point this at a stub so a full gate run
	// costs nothing. Unset in normal use, where the real `claude` CLI is used.
	if bin := os.Getenv("SLOPGATE_CLAUDE_BIN"); bin != "" {
		ck.Bin = bin
		logf("slopgate: using rule-check binary %s (SLOPGATE_CLAUDE_BIN)", bin)
	}

	r := runner.New(st, ck)
	r.Log = logf

	d := daemon.New(st, r)
	d.Log = logf

	// SIGINT/SIGTERM cancels the serve loop; in-flight runs are cancelled and
	// their records are written before the process exits.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logf("slopgate daemon starting (store %s)", st.Root)
	if err := d.Run(ctx); err != nil {
		return err
	}
	logf("slopgate daemon stopped")
	return nil
}
