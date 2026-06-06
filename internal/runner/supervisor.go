package runner

import (
	"context"
	"log"
	"sync"

	"lingobridge/internal/store"
)

// AccountStore is the account metadata needed by Supervisor.
type AccountStore interface {
	ListAccounts() ([]store.Account, error)
}

// MonitorRunner runs one account until ctx is canceled.
type MonitorRunner func(ctx context.Context, acc store.Account) error

// Supervisor keeps running monitors reconciled with enabled accounts in storage.
type Supervisor struct {
	store         AccountStore
	runMonitor    MonitorRunner
	targetAccount string

	mu      sync.Mutex
	wg      sync.WaitGroup
	running map[string]*runningMonitor
}

type runningMonitor struct {
	account   store.Account
	signature string
	cancel    context.CancelFunc
}

// NewSupervisor creates a supervisor. If targetAccount is non-empty, only that
// account name is eligible to run.
func NewSupervisor(st AccountStore, runMonitor MonitorRunner, targetAccount string) *Supervisor {
	return &Supervisor{
		store:         st,
		runMonitor:    runMonitor,
		targetAccount: targetAccount,
		running:       make(map[string]*runningMonitor),
	}
}

// Reconcile starts, stops, or restarts account monitors to match current store state.
func (s *Supervisor) Reconcile(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	accounts, err := s.store.ListAccounts()
	if err != nil {
		return err
	}

	desired := make(map[string]store.Account)
	for _, acc := range accounts {
		if !acc.Enabled {
			continue
		}
		if s.targetAccount != "" && acc.Name != s.targetAccount {
			continue
		}
		desired[acc.ID] = acc
	}

	s.mu.Lock()
	var toStop []*runningMonitor
	var toStart []store.Account

	for id, current := range s.running {
		next, ok := desired[id]
		if !ok || current.signature != accountSignature(next) {
			delete(s.running, id)
			toStop = append(toStop, current)
		}
	}

	for id, acc := range desired {
		if _, ok := s.running[id]; !ok {
			toStart = append(toStart, acc)
		}
	}

	s.mu.Unlock()

	for _, current := range toStop {
		current.cancel()
	}

	s.mu.Lock()
	if ctx.Err() == nil {
		for _, acc := range toStart {
			if _, ok := s.running[acc.ID]; !ok {
				s.startLocked(ctx, acc)
			}
		}
	}
	runningCount := len(s.running)
	s.mu.Unlock()

	log.Printf("[runner] reconciled accounts: running=%d", runningCount)
	return nil
}

// Stop cancels all running monitors and waits for them to exit.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	toStop := make([]*runningMonitor, 0, len(s.running))
	for id, current := range s.running {
		delete(s.running, id)
		toStop = append(toStop, current)
	}
	s.mu.Unlock()

	for _, current := range toStop {
		current.cancel()
	}
	s.wg.Wait()
}

// RunningCount returns the number of active account monitors.
func (s *Supervisor) RunningCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running)
}

func (s *Supervisor) startLocked(parent context.Context, acc store.Account) {
	ctx, cancel := context.WithCancel(parent)
	current := &runningMonitor{
		account:   acc,
		signature: accountSignature(acc),
		cancel:    cancel,
	}
	s.running[acc.ID] = current

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		log.Printf("[runner] starting account: %s (%s)", acc.Name, acc.ID)
		if err := s.runMonitor(ctx, acc); err != nil && ctx.Err() == nil {
			log.Printf("[runner] monitor for %s exited: %v", acc.Name, err)
		}

		s.mu.Lock()
		if s.running[acc.ID] == current {
			delete(s.running, acc.ID)
		}
		s.mu.Unlock()
	}()
}

func accountSignature(acc store.Account) string {
	return acc.ID + "\x00" + acc.Name + "\x00" + acc.Platform + "\x00" + acc.Token + "\x00" + acc.BaseURL + "\x00" + acc.UserID + "\x00" + acc.CredentialsJSON
}
