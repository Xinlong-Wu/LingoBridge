package runner

import (
	"context"
	"testing"
	"time"

	"wechatbox/internal/store"
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
