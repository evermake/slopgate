// Package daemon owns gate runs: it holds the singleton lock, serves the
// JSON-RPC socket, and executes runs out of process from any agent.
//
// Core constraint 2: slopgate is never a child of the agent process, so a run
// survives the agent's turn ending and the agent can neither manipulate nor
// kill it.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/evermake/slopgate/internal/git"
	"github.com/evermake/slopgate/internal/ipc"
	"github.com/evermake/slopgate/internal/model"
	"github.com/evermake/slopgate/internal/runner"
	"github.com/evermake/slopgate/internal/store"
)

// waitPollInterval backstops the in-memory completion broadcast, so a waiter
// still makes progress if a notification is ever missed.
const waitPollInterval = 500 * time.Millisecond

type Daemon struct {
	Store  *store.Store
	Runner *runner.Runner
	Log    func(format string, args ...any)

	mu      sync.Mutex
	waiters map[string][]chan struct{}
	active  map[string]context.CancelFunc
}

func New(s *store.Store, r *runner.Runner) *Daemon {
	return &Daemon{
		Store:   s,
		Runner:  r,
		waiters: map[string][]chan struct{}{},
		active:  map[string]context.CancelFunc{},
	}
}

func (d *Daemon) logf(format string, args ...any) {
	if d.Log != nil {
		d.Log(format, args...)
	}
}

// Run acquires the singleton lock and serves until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	release, err := d.Store.AcquireDaemonLock()
	if err != nil {
		return err
	}
	defer release()

	d.recoverInterruptedRuns()

	ln, err := ipc.Listen(d.Store.SocketPath())
	if err != nil {
		return fmt.Errorf("listen on %s: %w", d.Store.SocketPath(), err)
	}

	srv := ipc.NewServer()
	srv.Register("daemon.ping", d.handlePing)
	srv.Register("run.submit", d.handleSubmit)
	srv.Register("run.get", d.handleGet)
	srv.Register("run.wait", d.handleWait)

	d.logf("slopgate daemon listening on %s", d.Store.SocketPath())
	err = srv.Serve(ctx, ln)

	// Let in-flight runs finish writing their records before the process exits.
	d.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(d.active))
	for _, c := range d.active {
		cancels = append(cancels, c)
	}
	d.mu.Unlock()
	for _, c := range cancels {
		c()
	}
	return err
}

// recoverInterruptedRuns marks runs left `running` by a previous daemon as
// errored. The MVP does not resume them: a re-push retries, and a run claiming
// to be live with nothing executing it is worse than an honest error.
func (d *Daemon) recoverInterruptedRuns() {
	repos, err := d.Store.ListRepos()
	if err != nil {
		return
	}
	for _, repo := range repos {
		runs, err := d.Store.ListRuns(repo.ID)
		if err != nil {
			continue
		}
		for _, r := range runs {
			if r.State != model.StateRunning && r.State != model.StateQueued {
				continue
			}
			r.State = model.StateErrored
			r.Error = "interrupted by a daemon restart; push again to retry"
			now := time.Now()
			r.EndedAt = &now
			if err := d.Store.SaveRun(r); err == nil {
				d.logf("run %s: marked errored (interrupted)", shortSHA(r.SHA))
			}
		}
	}
}

func (d *Daemon) handlePing(_ context.Context, _ json.RawMessage) (any, error) {
	return map[string]bool{"ok": true}, nil
}

type submitParams struct {
	RepoID string `json:"repo_id"`
	Ref    string `json:"ref"`
	SHA    string `json:"sha"`
}

// handleSubmit registers the run durably before returning.
//
// This is what makes `git push && slopgate wait <sha>` race-free: git does not
// return until its hooks finish, so by the time the agent reads the SHA out of
// post-receive's output, the daemon already knows it. Firing an async
// notification instead would let `wait` arrive first and report an unknown SHA
// for a perfectly good gate. Registration is synchronous; the run is not.
func (d *Daemon) handleSubmit(_ context.Context, raw json.RawMessage) (any, error) {
	var p submitParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, ipc.Errorf(ipc.CodeInvalidParams, "bad params: %v", err)
	}
	repo, err := d.Store.GetRepo(p.RepoID)
	if err != nil {
		return nil, ipc.Errorf(ipc.CodeNotFound, "unknown repo %q; run `slopgate init` in it", p.RepoID)
	}

	run := &model.Run{
		ID:       store.NewID(),
		RepoID:   repo.ID,
		SHA:      p.SHA,
		Ref:      p.Ref,
		Branch:   git.BranchFromRef(p.Ref),
		State:    model.StateQueued,
		QueuedAt: time.Now(),
	}
	if err := d.Store.SaveRun(run); err != nil {
		return nil, fmt.Errorf("register run: %w", err)
	}
	d.logf("run %s: queued (%s on %s)", shortSHA(run.SHA), shortSHA(run.SHA), run.Branch)

	runCtx, cancel := context.WithCancel(context.Background())
	key := runKey(repo.ID, run.SHA)
	d.mu.Lock()
	d.active[key] = cancel
	d.mu.Unlock()

	go func() {
		defer cancel()
		d.Runner.Execute(runCtx, repo, run)
		d.mu.Lock()
		delete(d.active, key)
		d.mu.Unlock()
		d.notify(key)
	}()

	return map[string]string{"run_id": run.ID}, nil
}

type runRef struct {
	RepoID string `json:"repo_id"`
	SHA    string `json:"sha"`
}

func (d *Daemon) handleGet(_ context.Context, raw json.RawMessage) (any, error) {
	var p runRef
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, ipc.Errorf(ipc.CodeInvalidParams, "bad params: %v", err)
	}
	run, err := d.Store.GetRun(p.RepoID, p.SHA)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ipc.Errorf(ipc.CodeNotFound, "no gate run for %s; push it to the slopgate remote first", shortSHA(p.SHA))
		}
		return nil, err
	}
	return run, nil
}

type waitParams struct {
	RepoID    string `json:"repo_id"`
	SHA       string `json:"sha"`
	TimeoutMS int64  `json:"timeout_ms"`
}

func (d *Daemon) handleWait(ctx context.Context, raw json.RawMessage) (any, error) {
	var p waitParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, ipc.Errorf(ipc.CodeInvalidParams, "bad params: %v", err)
	}
	if p.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(p.TimeoutMS)*time.Millisecond)
		defer cancel()
	}

	key := runKey(p.RepoID, p.SHA)
	for {
		// Subscribe before reading, so a run completing between the read and
		// the wait cannot strand this caller until the poll interval.
		sub := d.subscribe(key)

		run, err := d.Store.GetRun(p.RepoID, p.SHA)
		if err != nil {
			d.unsubscribe(key, sub)
			if errors.Is(err, store.ErrNotFound) {
				return nil, ipc.Errorf(ipc.CodeNotFound, "no gate run for %s; push it to the slopgate remote first", shortSHA(p.SHA))
			}
			return nil, err
		}
		if run.State.Terminal() {
			d.unsubscribe(key, sub)
			return run, nil
		}

		select {
		case <-sub:
		case <-time.After(waitPollInterval):
			d.unsubscribe(key, sub)
		case <-ctx.Done():
			d.unsubscribe(key, sub)
			return nil, ipc.Errorf(ipc.CodeTimeout, "timed out waiting for %s; the run is still going and the verdict stays collectable with `slopgate status`", shortSHA(p.SHA))
		}
	}
}

func (d *Daemon) subscribe(key string) chan struct{} {
	ch := make(chan struct{})
	d.mu.Lock()
	d.waiters[key] = append(d.waiters[key], ch)
	d.mu.Unlock()
	return ch
}

func (d *Daemon) unsubscribe(key string, ch chan struct{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	subs := d.waiters[key]
	for i, c := range subs {
		if c == ch {
			d.waiters[key] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
	if len(d.waiters[key]) == 0 {
		delete(d.waiters, key)
	}
}

func (d *Daemon) notify(key string) {
	d.mu.Lock()
	subs := d.waiters[key]
	delete(d.waiters, key)
	d.mu.Unlock()
	for _, ch := range subs {
		close(ch)
	}
}

func runKey(repoID, sha string) string { return repoID + "/" + sha }

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
