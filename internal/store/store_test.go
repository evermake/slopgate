package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/evermake/slopgate/internal/model"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestOpen_CreatesTree(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.Root != root {
		t.Fatalf("Root = %q, want %q", s.Root, root)
	}
	for _, dir := range []string{root, filepath.Join(root, "repos"), filepath.Join(root, "worktrees"), filepath.Join(root, "runs")} {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if !fi.IsDir() {
			t.Fatalf("%s is not a directory", dir)
		}
	}
}

func TestOpen_EmptyUsesEnv(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SLOPGATE_HOME", root)

	s, err := Open("")
	if err != nil {
		t.Fatalf("Open(\"\"): %v", err)
	}
	if s.Root != root {
		t.Fatalf("Root = %q, want %q (from SLOPGATE_HOME)", s.Root, root)
	}
}

func TestDefaultRoot_FallsBackToHome(t *testing.T) {
	t.Setenv("SLOPGATE_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir available: %v", err)
	}
	got, err := DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	want := filepath.Join(home, ".slopgate")
	if got != want {
		t.Fatalf("DefaultRoot() = %q, want %q", got, want)
	}
}

func TestPathHelpers(t *testing.T) {
	s := &Store{Root: "/root"}

	if got, want := s.SocketPath(), "/root/daemon.sock"; got != want {
		t.Errorf("SocketPath() = %q, want %q", got, want)
	}
	if got, want := s.LockPath(), "/root/daemon.lock"; got != want {
		t.Errorf("LockPath() = %q, want %q", got, want)
	}
	if got, want := s.GateRepoPath("repo1"), "/root/repos/repo1.git"; got != want {
		t.Errorf("GateRepoPath() = %q, want %q", got, want)
	}
	if got, want := s.WorktreePath("repo1", "run1"), "/root/worktrees/repo1/run1"; got != want {
		t.Errorf("WorktreePath() = %q, want %q", got, want)
	}
	if got, want := s.RunDir("repo1", "sha1"), "/root/runs/repo1/sha1"; got != want {
		t.Errorf("RunDir() = %q, want %q", got, want)
	}
	if got, want := s.FeedbackPath("repo1", "sha1"), "/root/runs/repo1/sha1/feedback.md"; got != want {
		t.Errorf("FeedbackPath() = %q, want %q", got, want)
	}
	if got, want := s.LogPath("repo1", "sha1", model.ScriptCheck), "/root/runs/repo1/sha1/logs/check.log"; got != want {
		t.Errorf("LogPath() = %q, want %q", got, want)
	}
}

func TestNewID_LooksLikeULID(t *testing.T) {
	id := NewID()
	if len(id) != 26 {
		t.Fatalf("NewID() = %q, want length 26, got %d", id, len(id))
	}
	id2 := NewID()
	if id == id2 {
		t.Fatalf("NewID() returned the same value twice: %q", id)
	}
}

// --- Repo registry ---

func TestSaveRepo_GetRepo_RoundTrip(t *testing.T) {
	s := openTestStore(t)

	r := model.Repo{
		ID:            "01JB2X0000000000000000000",
		Path:          "/Users/me/code/myproject",
		GateRepo:      s.GateRepoPath("01JB2X0000000000000000000"),
		DefaultBranch: "main",
		UpstreamURL:   "git@github.com:me/myproject.git",
	}

	if err := s.SaveRepo(r); err != nil {
		t.Fatalf("SaveRepo: %v", err)
	}

	got, err := s.GetRepo(r.ID)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if got != r {
		t.Fatalf("GetRepo() = %+v, want %+v", got, r)
	}
}

func TestGetRepo_NotFound(t *testing.T) {
	s := openTestStore(t)
	_, err := s.GetRepo("nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRepo() err = %v, want ErrNotFound", err)
	}
}

func TestSaveRepo_UpsertDoesNotDuplicate(t *testing.T) {
	s := openTestStore(t)

	r := model.Repo{ID: "repo-a", Path: "/a", DefaultBranch: "main"}
	if err := s.SaveRepo(r); err != nil {
		t.Fatalf("SaveRepo (1st): %v", err)
	}

	r.DefaultBranch = "develop"
	if err := s.SaveRepo(r); err != nil {
		t.Fatalf("SaveRepo (2nd): %v", err)
	}

	repos, err := s.ListRepos()
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("ListRepos() = %d repos, want 1: %+v", len(repos), repos)
	}
	if repos[0].DefaultBranch != "develop" {
		t.Fatalf("DefaultBranch = %q, want %q (upsert should replace)", repos[0].DefaultBranch, "develop")
	}
}

func TestSaveRepo_ConcurrentDoesNotLoseRepos(t *testing.T) {
	s := openTestStore(t)

	const n = 50
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			r := model.Repo{
				ID:   NewID(),
				Path: filepath.Join("/repos", string(rune('a'+i%26)), NewID()),
			}
			errCh <- s.SaveRepo(r)
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("concurrent SaveRepo: %v", err)
		}
	}

	repos, err := s.ListRepos()
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != n {
		t.Fatalf("ListRepos() = %d repos, want %d (a concurrent SaveRepo lost an update)", len(repos), n)
	}
}

func TestFindRepoByPath_SymlinkAware(t *testing.T) {
	root := t.TempDir()
	s, err := Open(filepath.Join(root, "store"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	realDir := filepath.Join(root, "real-project")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	linkDir := filepath.Join(root, "linked-project")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}

	r := model.Repo{ID: "repo-sym", Path: realDir}
	if err := s.SaveRepo(r); err != nil {
		t.Fatalf("SaveRepo: %v", err)
	}

	// Looking up via the symlinked path must resolve to the same repo,
	// because the registered path and the lookup path refer to the same
	// underlying directory once symlinks are evaluated.
	got, err := s.FindRepoByPath(linkDir)
	if err != nil {
		t.Fatalf("FindRepoByPath(linkDir): %v", err)
	}
	if got.ID != r.ID {
		t.Fatalf("FindRepoByPath(linkDir).ID = %q, want %q", got.ID, r.ID)
	}

	got2, err := s.FindRepoByPath(realDir)
	if err != nil {
		t.Fatalf("FindRepoByPath(realDir): %v", err)
	}
	if got2.ID != r.ID {
		t.Fatalf("FindRepoByPath(realDir).ID = %q, want %q", got2.ID, r.ID)
	}
}

func TestFindRepoByPath_NotFound(t *testing.T) {
	s := openTestStore(t)
	_, err := s.FindRepoByPath(t.TempDir())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("FindRepoByPath() err = %v, want ErrNotFound", err)
	}
}

// --- Runs ---

func fullRun(repoID, sha string) *model.Run {
	now := time.Now().UTC().Truncate(time.Millisecond)
	started := now.Add(time.Second)
	ended := now.Add(time.Minute)
	return &model.Run{
		ID:            "run-1",
		RepoID:        repoID,
		SHA:           sha,
		Ref:           "refs/heads/feat/x",
		Branch:        "feat/x",
		State:         model.StateFailed,
		BaseSHA:       "base123",
		DefaultBranch: "main",
		FetchWarning:  "",
		ChangedFiles:  []string{"src/a.go", "src/b.go"},
		Drift: []model.DriftEntry{
			{Status: "M", Path: ".slopgate/scripts/check.sh"},
			{Status: "D", Path: ".slopgate/rules/no-any.md"},
		},
		Scripts: map[model.ScriptName]model.ScriptResult{
			model.ScriptCheck: {Name: model.ScriptCheck, ExitCode: 1, DurationMS: 120, LogPath: "logs/check.log"},
			model.ScriptTest:  {Name: model.ScriptTest, Skipped: true},
		},
		Rules: []model.RuleResult{
			{
				Name: "no-type-casts",
				Findings: []model.Finding{
					{Rule: "no-type-casts", File: "src/a.go", Line: 42, Snippet: "x := y.(int)", Explanation: "avoid type assertions"},
				},
				Discarded: []model.DiscardedFinding{
					{
						Finding: model.Finding{Rule: "no-type-casts", File: "src/c.go", Line: 1, Snippet: "z", Explanation: "n/a"},
						Reason:  model.DiscardNotChanged,
					},
				},
				DurationMS: 5000,
			},
		},
		Warnings:     []string{"something noteworthy"},
		QueuedAt:     now,
		StartedAt:    &started,
		EndedAt:      &ended,
		FeedbackPath: "runs/repo1/sha1/feedback.md",
	}
}

func TestSaveRun_GetRun_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	r := fullRun("repo1", "sha1")

	if err := s.SaveRun(r); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	got, err := s.GetRun("repo1", "sha1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}

	if got.ID != r.ID || got.RepoID != r.RepoID || got.SHA != r.SHA || got.State != r.State {
		t.Fatalf("GetRun() basic fields mismatch: got %+v, want %+v", got, r)
	}
	if len(got.Drift) != 2 || got.Drift[0] != r.Drift[0] || got.Drift[1] != r.Drift[1] {
		t.Fatalf("GetRun() Drift mismatch: got %+v, want %+v", got.Drift, r.Drift)
	}
	if len(got.Rules) != 1 || len(got.Rules[0].Findings) != 1 || len(got.Rules[0].Discarded) != 1 {
		t.Fatalf("GetRun() Rules mismatch: %+v", got.Rules)
	}
	if got.Rules[0].Findings[0] != r.Rules[0].Findings[0] {
		t.Fatalf("Finding mismatch: got %+v, want %+v", got.Rules[0].Findings[0], r.Rules[0].Findings[0])
	}
	if got.Rules[0].Discarded[0] != r.Rules[0].Discarded[0] {
		t.Fatalf("Discarded mismatch: got %+v, want %+v", got.Rules[0].Discarded[0], r.Rules[0].Discarded[0])
	}
	if len(got.Scripts) != 2 {
		t.Fatalf("Scripts mismatch: %+v", got.Scripts)
	}
	if got.Scripts[model.ScriptCheck].ExitCode != 1 {
		t.Fatalf("Scripts[check].ExitCode = %d, want 1", got.Scripts[model.ScriptCheck].ExitCode)
	}
	if !got.Scripts[model.ScriptTest].Skipped {
		t.Fatalf("Scripts[test].Skipped = false, want true")
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(*r.StartedAt) {
		t.Fatalf("StartedAt mismatch: got %v, want %v", got.StartedAt, r.StartedAt)
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(*r.EndedAt) {
		t.Fatalf("EndedAt mismatch: got %v, want %v", got.EndedAt, r.EndedAt)
	}
	if !got.QueuedAt.Equal(r.QueuedAt) {
		t.Fatalf("QueuedAt mismatch: got %v, want %v", got.QueuedAt, r.QueuedAt)
	}
	if len(got.Warnings) != 1 || got.Warnings[0] != "something noteworthy" {
		t.Fatalf("Warnings mismatch: %+v", got.Warnings)
	}
	if got.FeedbackPath != r.FeedbackPath {
		t.Fatalf("FeedbackPath mismatch: got %q, want %q", got.FeedbackPath, r.FeedbackPath)
	}
}

func TestGetRun_NotFound(t *testing.T) {
	s := openTestStore(t)
	_, err := s.GetRun("repo1", "nonexistent-sha")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRun() err = %v, want ErrNotFound", err)
	}
}

func TestListRuns(t *testing.T) {
	s := openTestStore(t)

	base := time.Now().UTC().Truncate(time.Second)
	r1 := &model.Run{ID: "1", RepoID: "repo1", SHA: "sha-older", State: model.StatePassed, QueuedAt: base}
	r2 := &model.Run{ID: "2", RepoID: "repo1", SHA: "sha-newer", State: model.StatePassed, QueuedAt: base.Add(time.Hour)}

	if err := s.SaveRun(r2); err != nil {
		t.Fatalf("SaveRun r2: %v", err)
	}
	if err := s.SaveRun(r1); err != nil {
		t.Fatalf("SaveRun r1: %v", err)
	}

	runs, err := s.ListRuns("repo1")
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("ListRuns() = %d runs, want 2", len(runs))
	}
	if runs[0].SHA != "sha-older" || runs[1].SHA != "sha-newer" {
		t.Fatalf("ListRuns() not sorted by QueuedAt ascending: %v, %v", runs[0].SHA, runs[1].SHA)
	}
}

func TestListRuns_EmptyForUnknownRepo(t *testing.T) {
	s := openTestStore(t)
	runs, err := s.ListRuns("no-such-repo")
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("ListRuns() = %d runs, want 0", len(runs))
	}
}

// --- Feedback & rule raw output ---

func TestWriteFeedback_ReadFeedback(t *testing.T) {
	s := openTestStore(t)

	path, err := s.WriteFeedback("repo1", "sha1", "# slopgate: FAILED\n")
	if err != nil {
		t.Fatalf("WriteFeedback: %v", err)
	}
	if path != s.FeedbackPath("repo1", "sha1") {
		t.Fatalf("WriteFeedback path = %q, want %q", path, s.FeedbackPath("repo1", "sha1"))
	}

	got, err := s.ReadFeedback("repo1", "sha1")
	if err != nil {
		t.Fatalf("ReadFeedback: %v", err)
	}
	if got != "# slopgate: FAILED\n" {
		t.Fatalf("ReadFeedback() = %q, want %q", got, "# slopgate: FAILED\n")
	}
}

func TestReadFeedback_NotFound(t *testing.T) {
	s := openTestStore(t)
	_, err := s.ReadFeedback("repo1", "sha-without-feedback")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadFeedback() err = %v, want ErrNotFound", err)
	}
}

func TestWriteRuleRaw(t *testing.T) {
	s := openTestStore(t)
	data := []byte(`{"structured_output":{"findings":[]},"usage":{}}`)

	if err := s.WriteRuleRaw("repo1", "sha1", "no-any", data); err != nil {
		t.Fatalf("WriteRuleRaw: %v", err)
	}

	path := filepath.Join(s.RunDir("repo1", "sha1"), "rules", "no-any.json")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != string(data) {
		t.Fatalf("WriteRuleRaw content = %q, want %q", got, data)
	}
}

// --- Log writer & TailLog ---

func TestOpenLogWriter_TailKeeping(t *testing.T) {
	s := openTestStore(t)

	w, err := s.OpenLogWriter("repo1", "sha1", model.ScriptCheck)
	if err != nil {
		t.Fatalf("OpenLogWriter: %v", err)
	}

	// Write well over 1 MiB, in chunks, with a distinguishable head and tail.
	head := []byte("HEAD-MARKER-")
	if _, err := w.Write(head); err != nil {
		t.Fatalf("Write head: %v", err)
	}

	chunk := make([]byte, 64*1024)
	for i := range chunk {
		chunk[i] = 'x'
	}
	// 20 * 64KiB = 1.25 MiB of filler, comfortably over the 1 MiB cap.
	for i := 0; i < 20; i++ {
		if _, err := w.Write(chunk); err != nil {
			t.Fatalf("Write chunk %d: %v", i, err)
		}
	}

	tail := []byte("TAIL-MARKER-END")
	if _, err := w.Write(tail); err != nil {
		t.Fatalf("Write tail: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	path := s.LogPath("repo1", "sha1", model.ScriptCheck)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	if len(data) != logCapBytes {
		t.Fatalf("log file size = %d, want exactly %d (the cap)", len(data), logCapBytes)
	}
	if !hasSuffix(data, tail) {
		t.Fatalf("log file does not end with the tail marker; last 32 bytes: %q", data[len(data)-32:])
	}
	if hasPrefix(data, head) || contains(data, head) {
		t.Fatalf("log file still contains the head marker; it should have been dropped")
	}
}

func hasSuffix(data, suffix []byte) bool {
	if len(data) < len(suffix) {
		return false
	}
	return string(data[len(data)-len(suffix):]) == string(suffix)
}

func hasPrefix(data, prefix []byte) bool {
	if len(data) < len(prefix) {
		return false
	}
	return string(data[:len(prefix)]) == string(prefix)
}

func contains(data, sub []byte) bool {
	return len(sub) == 0 || indexOf(data, sub) >= 0
}

func indexOf(data, sub []byte) int {
	if len(sub) > len(data) {
		return -1
	}
	for i := 0; i+len(sub) <= len(data); i++ {
		if string(data[i:i+len(sub)]) == string(sub) {
			return i
		}
	}
	return -1
}

func TestOpenLogWriter_ConcurrentWrites(t *testing.T) {
	s := openTestStore(t)
	w, err := s.OpenLogWriter("repo1", "sha1", model.ScriptTest)
	if err != nil {
		t.Fatalf("OpenLogWriter: %v", err)
	}

	const goroutines = 8
	const writesEach = 200
	done := make(chan struct{}, goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			for i := 0; i < writesEach; i++ {
				_, _ = w.Write([]byte("line\n"))
			}
			done <- struct{}{}
		}()
	}
	for g := 0; g < goroutines; g++ {
		<-done
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	path := s.LogPath("repo1", "sha1", model.ScriptTest)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	wantLen := goroutines * writesEach * len("line\n")
	if len(data) != wantLen {
		t.Fatalf("log length = %d, want %d (concurrent writes lost data)", len(data), wantLen)
	}
}

func TestTailLog(t *testing.T) {
	s := openTestStore(t)
	w, err := s.OpenLogWriter("repo1", "sha1", model.ScriptCheck)
	if err != nil {
		t.Fatalf("OpenLogWriter: %v", err)
	}
	for i := 1; i <= 10; i++ {
		_, _ = w.Write([]byte("line " + itoa(i) + "\n"))
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := s.TailLog("repo1", "sha1", model.ScriptCheck, 3)
	if err != nil {
		t.Fatalf("TailLog: %v", err)
	}
	want := "line 8\nline 9\nline 10"
	if got != want {
		t.Fatalf("TailLog() = %q, want %q", got, want)
	}
}

func TestTailLog_NotFound(t *testing.T) {
	s := openTestStore(t)
	_, err := s.TailLog("repo1", "sha-no-log", model.ScriptSetup, 10)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("TailLog() err = %v, want ErrNotFound", err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// --- Path validation ---

func TestValidation_RejectsTraversal(t *testing.T) {
	s := openTestStore(t)

	badIDs := []string{"../escape", "..", ".", "a/b", "a\\b", "a/../b", ""}
	for _, id := range badIDs {
		if _, err := s.GetRun(id, "sha1"); err == nil {
			t.Errorf("GetRun(repoID=%q, ...) succeeded, want validation error", id)
		}
	}

	for _, sha := range badIDs {
		if _, err := s.GetRun("repo1", sha); err == nil {
			t.Errorf("GetRun(sha=%q) succeeded, want validation error", sha)
		}
	}

	if err := s.WriteRuleRaw("repo1", "sha1", "../escape", []byte("{}")); err == nil {
		t.Errorf("WriteRuleRaw(rule=..%q) succeeded, want validation error", "../escape")
	}

	if _, err := s.OpenLogWriter("../escape", "sha1", model.ScriptCheck); err == nil {
		t.Errorf("OpenLogWriter(repoID=../escape) succeeded, want validation error")
	}
}

func TestValidation_DoesNotEscapeRoot(t *testing.T) {
	s := openTestStore(t)

	// Even if validation had a hole, confirm no file ever lands outside Root
	// for a representative set of malicious-looking but distinct inputs.
	_ = s.SaveRun(&model.Run{ID: "x", RepoID: "../../etc", SHA: "passwd", QueuedAt: time.Now()})

	outside := filepath.Join(filepath.Dir(s.Root), "etc")
	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("a file was created outside the store root at %s", outside)
	}
}

// --- Daemon lock ---

func TestAcquireDaemonLock_SecondAcquisitionFails(t *testing.T) {
	s := openTestStore(t)

	release1, err := s.AcquireDaemonLock()
	if err != nil {
		t.Fatalf("AcquireDaemonLock (1st): %v", err)
	}

	_, err = s.AcquireDaemonLock()
	if !errors.Is(err, ErrDaemonRunning) {
		t.Fatalf("AcquireDaemonLock (2nd) err = %v, want ErrDaemonRunning", err)
	}

	if err := release1(); err != nil {
		t.Fatalf("release1: %v", err)
	}

	release2, err := s.AcquireDaemonLock()
	if err != nil {
		t.Fatalf("AcquireDaemonLock (after release): %v", err)
	}
	if err := release2(); err != nil {
		t.Fatalf("release2: %v", err)
	}
}

func TestAcquireDaemonLock_DoesNotRemoveLockFile(t *testing.T) {
	s := openTestStore(t)

	release, err := s.AcquireDaemonLock()
	if err != nil {
		t.Fatalf("AcquireDaemonLock: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	if _, err := os.Stat(s.LockPath()); err != nil {
		t.Fatalf("lock file removed on release, want it to persist: %v", err)
	}
}
