package main

import (
	"context"
	"log"
	"sync"
	"time"
)

type MiningStatus struct {
	Active      bool      `json:"active"`
	NextAttempt time.Time `json:"next_attempt"`
	LastAttempt time.Time `json:"last_attempt"`
	LastError   string    `json:"last_error,omitempty"`
	LastBlock   int       `json:"last_block"`
}
type miningMonitor struct {
	mu    sync.RWMutex
	state MiningStatus
}

func (m *miningMonitor) snapshot() MiningStatus { m.mu.RLock(); defer m.mu.RUnlock(); return m.state }
func (m *miningMonitor) update(fn func(*MiningStatus)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(&m.state)
}
func mineLoop(ctx context.Context, bc *Blockchain, miner string, interval time.Duration, done chan<- struct{}) {
	mineLoopMonitored(ctx, bc, miner, interval, nil, done)
}
func mineLoopMonitored(ctx context.Context, bc *Blockchain, miner string, interval time.Duration, monitor *miningMonitor, done chan<- struct{}) {
	defer close(done)
	mine := func(next time.Time) {
		monitor.update(func(s *MiningStatus) {
			s.Active = true
			s.LastAttempt = time.Now().UTC()
			s.NextAttempt = next
			s.LastError = ""
		})
		b, err := bc.MinePending(miner)
		monitor.update(func(s *MiningStatus) {
			s.Active = false
			if err != nil {
				s.LastError = err.Error()
			} else {
				s.LastBlock = b.Index
			}
		})
		if err != nil {
			log.Printf("Mining failed: %v", err)
			return
		}
		log.Printf("Mined block #%d: +%s GO to %s", b.Index, formatAmount(b.Reward), miner)
	}
	mine(time.Time{})
	next := time.Now().Add(interval)
	monitor.update(func(s *MiningStatus) { s.NextAttempt = next.UTC() })
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer monitor.update(func(s *MiningStatus) { s.Active = false; s.NextAttempt = time.Time{} })
	for {
		select {
		case <-ctx.Done():
			return
		case tick := <-ticker.C:
			mine(tick.Add(interval).UTC())
		}
	}
}
