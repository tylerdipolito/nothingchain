package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const MaxPeerBlocks = 10000
const MaxPeerBytes = 8 << 20

func (bc *Blockchain) Snapshot() chainState {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	s := copyState(bc.state)
	for i, b := range s.Blocks {
		s.Blocks[i] = cloneBlock(b)
	}
	return s
}

// Every block has the same required work in this experiment. Prefer more blocks;
// break equal-work ties by the lexicographically smaller tip hash so peers converge.
func betterChain(candidate, current []*Block) bool {
	if len(candidate) != len(current) {
		return len(candidate) > len(current)
	}
	return candidate[len(candidate)-1].Hash < current[len(current)-1].Hash
}

func (bc *Blockchain) Adopt(remote chainState) (bool, error) {
	if len(remote.Blocks) == 0 || len(remote.Blocks) > MaxPeerBlocks || len(remote.Pending) > MaxPendingTxs {
		return false, fmt.Errorf("peer snapshot exceeds bounds or has no blocks")
	}
	if _, err := validateState(remote, false); err != nil {
		return false, fmt.Errorf("reject peer chain: %w", err)
	}
	if err := bc.checkCheckpoint(remote, true); err != nil {
		return false, err
	}
	// Take ownership of all candidate data before publication.
	candidate := copyState(remote)
	for i, b := range candidate.Blocks {
		candidate.Blocks[i] = cloneBlock(b)
	}
	bc.mu.Lock()
	defer bc.mu.Unlock()
	old := bc.state
	s := copyState(old)
	adopted := betterChain(candidate.Blocks, old.Blocks)
	pool := append([]Transaction{}, old.Pending...)
	if adopted {
		common := 0
		for common < len(old.Blocks) && common < len(candidate.Blocks) && old.Blocks[common].Hash == candidate.Blocks[common].Hash {
			common++
		}
		s.Blocks = candidate.Blocks
		for _, b := range old.Blocks[common:] {
			for _, tx := range b.Txs {
				if len(pool) >= 2*MaxPendingTxs {
					break
				}
				pool = append(pool, tx)
			}
			if len(pool) >= 2*MaxPendingTxs {
				break
			}
		}
	}
	pool = append(pool, candidate.Pending...)
	l, err := validateState(s, false)
	if err != nil {
		return false, err
	}
	// Pending operations execute in the next block, just as in AddTx. Using
	// the confirmed tip height here can resurrect expired AMM transactions.
	l.height = int64(len(s.Blocks))
	s.Pending = []Transaction{}
	// Retry nonce-dependent entries after predecessors; ignore invalid or already
	// confirmed transactions. apply must leave the ledger unchanged on failure.
	for len(pool) > 0 && len(s.Pending) < MaxPendingTxs {
		waiting := make([]Transaction, 0, len(pool))
		progress := false
		for _, tx := range pool {
			if len(s.Pending) >= MaxPendingTxs {
				break
			}
			if err := l.apply(tx); err != nil {
				waiting = append(waiting, tx)
				continue
			}
			s.Pending = append(s.Pending, tx)
			progress = true
		}
		if !progress {
			break
		}
		pool = waiting
	}
	changed := adopted || len(s.Pending) != len(old.Pending)
	if !changed {
		for i, tx := range s.Pending {
			if tx != old.Pending[i] {
				changed = true
				break
			}
		}
	}
	if !changed {
		return false, nil
	}
	if err := bc.checkCheckpoint(s, true); err != nil {
		return false, err
	}
	if err := bc.persist(s); err != nil {
		return false, fmt.Errorf("save peer state: %w", err)
	}
	bc.state = s
	return adopted, nil
}

type PeerStatus struct {
	URL           string `json:"url"`
	LastSync      string `json:"last_sync,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	DurationMS    int64  `json:"round_trip_ms"`
	RemoteHeight  int    `json:"remote_height"`
	RemoteTip     string `json:"remote_tip,omitempty"`
	AdoptedChains int    `json:"adopted_chains"`
}
type PeerManager struct {
	bc     *Blockchain
	mu     sync.RWMutex
	peers  []PeerStatus
	client *http.Client
}

func newPeerManager(bc *Blockchain, urls []string) (*PeerManager, error) {
	p := &PeerManager{bc: bc, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error { return fmt.Errorf("peer redirects are disabled") }}}
	seen := map[string]bool{}
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return nil, fmt.Errorf("peer must be a loopback HTTP origin")
		}
		ip := net.ParseIP(u.Hostname())
		if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, fmt.Errorf("this experiment only accepts loopback peers")
		}
		endpoint := strings.TrimRight(u.String(), "/")
		if !seen[endpoint] {
			p.peers = append(p.peers, PeerStatus{URL: endpoint})
			seen[endpoint] = true
		}
	}
	if len(p.peers) > 8 {
		return nil, fmt.Errorf("at most 8 local peers are supported")
	}
	return p, nil
}
func (p *PeerManager) Status() []PeerStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]PeerStatus{}, p.peers...)
}
func (p *PeerManager) syncPeer(ctx context.Context, endpoint string) (chainState, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/sync", nil)
	if err != nil {
		return chainState{}, err
	}
	response, err := p.client.Do(req)
	if err != nil {
		return chainState{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return chainState{}, fmt.Errorf("peer HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxPeerBytes+1))
	if err != nil {
		return chainState{}, err
	}
	if len(data) > MaxPeerBytes {
		return chainState{}, fmt.Errorf("peer response exceeds %d bytes", MaxPeerBytes)
	}
	var s chainState
	if err = json.Unmarshal(data, &s); err != nil {
		return s, err
	}
	return s, nil
}
func (p *PeerManager) SyncOnce(ctx context.Context) {
	for i, peer := range p.Status() {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		snapshot, err := p.syncPeer(ctx, peer.URL)
		adopted := false
		if err == nil {
			adopted, err = p.bc.Adopt(snapshot)
		}
		peer.DurationMS = time.Since(start).Milliseconds()
		if err != nil {
			peer.LastError = err.Error()
		} else {
			peer.LastError = ""
			peer.LastSync = time.Now().UTC().Format(time.RFC3339Nano)
			peer.RemoteHeight = len(snapshot.Blocks) - 1
			peer.RemoteTip = snapshot.Blocks[len(snapshot.Blocks)-1].Hash
			if adopted {
				peer.AdoptedChains++
				log.Printf("Accepted peer chain from %s at height %d", peer.URL, peer.RemoteHeight)
			}
		}
		p.mu.Lock()
		p.peers[i] = peer
		p.mu.Unlock()
	}
}
func (p *PeerManager) Run(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	p.SyncOnce(ctx)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.SyncOnce(ctx)
		}
	}
}
