package runner

import (
	"context"
	"errors"
	"testing"
	"time"

	"lingobridge/internal/store"
)

type fakeAccountStore struct {
	accounts []store.Account
}

func (f *fakeAccountStore) ListAccounts() ([]store.Account, error) {
	out := make([]store.Account, len(f.accounts))
	copy(out, f.accounts)
	return out, nil
}

type recordingRunner struct {
	starts chan store.Account
	stops  chan string
}

func newRecordingRunner() *recordingRunner {
	return &recordingRunner{
		starts: make(chan store.Account, 10),
		stops:  make(chan string, 10),
	}
}

func (r *recordingRunner) run(ctx context.Context, acc store.Account) error {
	r.starts <- acc
	<-ctx.Done()
	r.stops <- acc.ID
	return nil
}

func waitStart(t *testing.T, ch <-chan store.Account) store.Account {
	t.Helper()
	select {
	case acc := <-ch:
		return acc
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for start")
		return store.Account{}
	}
}

func waitStartID(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case id := <-ch:
		return id
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for start")
		return ""
	}
}

func waitStop(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case id := <-ch:
		return id
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stop")
		return ""
	}
}

func waitMonitorExit(t *testing.T, ch <-chan MonitorExit) MonitorExit {
	t.Helper()
	select {
	case exit := <-ch:
		return exit
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for monitor exit")
		return MonitorExit{}
	}
}

func assertNoStart(t *testing.T, ch <-chan store.Account) {
	t.Helper()
	select {
	case acc := <-ch:
		t.Fatalf("unexpected start: %#v", acc)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestSupervisorStartsNewAccounts(t *testing.T) {
	st := &fakeAccountStore{accounts: []store.Account{{ID: "a1", Name: "bot", Token: "t", Enabled: true}}}
	runner := newRecordingRunner()
	s := NewSupervisor(st, runner.run, "")
	defer s.Stop()

	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if got := waitStart(t, runner.starts).ID; got != "a1" {
		t.Fatalf("started account = %q, want a1", got)
	}
	if got := s.RunningCount(); got != 1 {
		t.Fatalf("RunningCount = %d, want 1", got)
	}
}

func TestSupervisorStopsDeletedOrDisabledAccounts(t *testing.T) {
	st := &fakeAccountStore{accounts: []store.Account{{ID: "a1", Name: "bot", Token: "t", Enabled: true}}}
	runner := newRecordingRunner()
	s := NewSupervisor(st, runner.run, "")
	defer s.Stop()

	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	waitStart(t, runner.starts)

	st.accounts = nil
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if got := waitStop(t, runner.stops); got != "a1" {
		t.Fatalf("stopped account = %q, want a1", got)
	}
	if got := s.RunningCount(); got != 0 {
		t.Fatalf("RunningCount = %d, want 0", got)
	}
}

func TestSupervisorRestartsChangedAccount(t *testing.T) {
	st := &fakeAccountStore{accounts: []store.Account{{ID: "a1", Name: "bot", Token: "old", BaseURL: "base", Enabled: true}}}
	runner := newRecordingRunner()
	s := NewSupervisor(st, runner.run, "")
	defer s.Stop()

	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	waitStart(t, runner.starts)

	st.accounts = []store.Account{{ID: "a1", Name: "bot", Token: "new", BaseURL: "base", Enabled: true}}
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if got := waitStop(t, runner.stops); got != "a1" {
		t.Fatalf("stopped account = %q, want a1", got)
	}
	if got := waitStart(t, runner.starts).Token; got != "new" {
		t.Fatalf("restarted token = %q, want new", got)
	}
}

func TestSupervisorRestartsChangedAccountMetadata(t *testing.T) {
	st := &fakeAccountStore{accounts: []store.Account{{
		ID:       "feishu:cli_xxx",
		Name:     "fsbot",
		Platform: store.PlatformFeishu,
		BaseURL:  "https://open.feishu.cn",
		Enabled:  true,
	}}}
	runner := newRecordingRunner()
	supervisor := NewSupervisor(st, runner.run, "")
	defer supervisor.Stop()

	ctx := context.Background()
	if err := supervisor.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	first := waitStart(t, runner.starts)

	st.accounts = []store.Account{{
		ID:       "feishu:cli_xxx",
		Name:     "fsbot-renamed",
		Platform: store.PlatformFeishu,
		BaseURL:  "https://open.feishu.cn",
		Enabled:  true,
	}}
	if err := supervisor.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile after metadata change returned error: %v", err)
	}
	second := waitStart(t, runner.starts)
	if first.Name == second.Name {
		t.Fatalf("account metadata did not change: %q", second.Name)
	}
}

func TestSupervisorRestartsWhenSignatureExtraChanges(t *testing.T) {
	st := &fakeAccountStore{accounts: []store.Account{{
		ID:       "feishu:cli_xxx",
		Name:     "fsbot",
		Platform: store.PlatformFeishu,
		Enabled:  true,
	}}}
	runner := newRecordingRunner()
	supervisor := NewSupervisor(st, runner.run, "")
	defer supervisor.Stop()

	extra := "old"
	supervisor.SetSignatureExtra(func(store.Account) string { return extra })
	if err := supervisor.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	waitStart(t, runner.starts)

	extra = "new"
	if err := supervisor.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile after signature change returned error: %v", err)
	}
	if got := waitStop(t, runner.stops); got != "feishu:cli_xxx" {
		t.Fatalf("stopped account = %q, want feishu:cli_xxx", got)
	}
	waitStart(t, runner.starts)
}

func TestSupervisorTargetAccountFilter(t *testing.T) {
	st := &fakeAccountStore{accounts: []store.Account{
		{ID: "a1", Name: "wanted", Token: "t", Enabled: true},
		{ID: "a2", Name: "other", Token: "t", Enabled: true},
	}}
	runner := newRecordingRunner()
	s := NewSupervisor(st, runner.run, "wanted")
	defer s.Stop()

	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if got := waitStart(t, runner.starts).ID; got != "a1" {
		t.Fatalf("started account = %q, want a1", got)
	}
	assertNoStart(t, runner.starts)
}

func TestSupervisorReportsSingleMonitorFailure(t *testing.T) {
	wantErr := errors.New("bad config")
	st := &fakeAccountStore{accounts: []store.Account{{ID: "a1", Name: "bot", Platform: store.PlatformFeishu, Enabled: true}}}
	exits := make(chan MonitorExit, 1)
	s := NewSupervisor(st, func(context.Context, store.Account) error {
		return wantErr
	}, "")
	s.SetMonitorExitHandler(func(exit MonitorExit) {
		exits <- exit
	})
	defer s.Stop()

	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	exit := waitMonitorExit(t, exits)
	if !errors.Is(exit.Err, wantErr) {
		t.Fatalf("exit.Err = %v, want %v", exit.Err, wantErr)
	}
	if exit.Account.ID != "a1" || exit.RemainingRunning != 0 {
		t.Fatalf("exit = %#v, want account a1 and no remaining monitors", exit)
	}
	if got := s.RunningCount(); got != 0 {
		t.Fatalf("RunningCount = %d, want 0", got)
	}
}

func TestSupervisorReportsRemainingRunningOnMonitorFailure(t *testing.T) {
	wantErr := errors.New("bad config")
	st := &fakeAccountStore{accounts: []store.Account{
		{ID: "fail", Name: "bad", Platform: store.PlatformFeishu, Enabled: true},
		{ID: "live", Name: "good", Platform: store.PlatformWeChat, Enabled: true},
	}}
	starts := make(chan string, 2)
	stops := make(chan string, 1)
	exits := make(chan MonitorExit, 1)
	s := NewSupervisor(st, func(ctx context.Context, acc store.Account) error {
		starts <- acc.ID
		if acc.ID == "fail" {
			return wantErr
		}
		<-ctx.Done()
		stops <- acc.ID
		return nil
	}, "")
	s.SetMonitorExitHandler(func(exit MonitorExit) {
		exits <- exit
	})
	defer s.Stop()

	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	waitStartID(t, starts)
	waitStartID(t, starts)
	exit := waitMonitorExit(t, exits)
	if !errors.Is(exit.Err, wantErr) || exit.Account.ID != "fail" || exit.RemainingRunning != 1 {
		t.Fatalf("exit = %#v, want failed account with one remaining monitor", exit)
	}
	if got := s.RunningCount(); got != 1 {
		t.Fatalf("RunningCount = %d, want 1", got)
	}

	s.Stop()
	if got := waitStop(t, stops); got != "live" {
		t.Fatalf("stopped account = %q, want live", got)
	}
}
