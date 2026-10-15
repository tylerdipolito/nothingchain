package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type chainState struct {
	ChainID string        `json:"chain_id"`
	Blocks  []*Block      `json:"blocks"`
	Pending []Transaction `json:"pending"`
}
type Blockchain struct {
	checkpoint *Checkpoint
	mu         sync.RWMutex
	state      chainState
	path       string
}
type ledger struct {
	amm         *ammState
	height      int64
	balances    map[string]int64
	nonces      map[string]uint64
	supply      int64
	transferred int64
}

var genesisOnce sync.Once
var genesisHash string

func expectedGenesisHash() string {
	genesisOnce.Do(func() { genesisHash = GenesisBlock().Hash })
	return genesisHash
}

func newLedger() *ledger {
	return &ledger{amm: newAMMState(), balances: map[string]int64{}, nonces: map[string]uint64{}}
}
func (l *ledger) apply(tx Transaction) error {
	if err := tx.Validate(); err != nil {
		return err
	}
	if l.nonces[tx.From] == math.MaxUint64 || tx.Nonce != l.nonces[tx.From]+1 {
		return fmt.Errorf("invalid nonce: expected next nonce after %d", l.nonces[tx.From])
	}
	if AMMEnabled && strings.HasPrefix(tx.Memo, "amm:") {
		if err := l.applyAMM(tx); err != nil {
			return err
		}
		l.nonces[tx.From] = tx.Nonce
		return nil
	}
	if l.balances[tx.From] < tx.Amount {
		return fmt.Errorf("insufficient balance")
	}
	if l.transferred > math.MaxInt64-tx.Amount {
		return fmt.Errorf("transfer counter overflow")
	}
	if tx.From != tx.To {
		if l.balances[tx.To] > math.MaxInt64-tx.Amount {
			return fmt.Errorf("recipient balance overflow")
		}
		l.balances[tx.From] -= tx.Amount
		l.balances[tx.To] += tx.Amount
	}
	l.transferred += tx.Amount
	l.nonces[tx.From] = tx.Nonce
	return nil
}
func validateState(s chainState, includePending bool) (*ledger, error) {
	return validateStateAt(s, includePending, time.Now().Unix())
}
func validateStateAt(s chainState, includePending bool, now int64) (*ledger, error) {
	if s.ChainID != ChainID {
		return nil, fmt.Errorf("wrong chain ID: expected %s", ChainID)
	}
	if len(s.Blocks) == 0 {
		return nil, fmt.Errorf("empty chain")
	}
	l := newLedger()
	for i, b := range s.Blocks {
		if b == nil {
			return nil, fmt.Errorf("block %d is null", i)
		}
		if b.Index != i || b.Nonce < 0 || b.Timestamp > now+MaxFutureSeconds {
			return nil, fmt.Errorf("block %d: invalid index or nonce", i)
		}
		if b.Hash != b.computeHash() || !strings.HasPrefix(b.Hash, strings.Repeat("0", Difficulty)) {
			return nil, fmt.Errorf("block %d: invalid hash or proof of work", i)
		}
		if i == 0 {
			if b.Hash != expectedGenesisHash() {
				return nil, fmt.Errorf("invalid genesis")
			}
			continue
		}
		if b.PrevHash != s.Blocks[i-1].Hash || b.Timestamp < s.Blocks[i-1].Timestamp {
			return nil, fmt.Errorf("block %d: invalid previous hash or timestamp", i)
		}
		if b.Reward != rewardAt(b.Timestamp) || !validAddress(b.Miner) {
			return nil, fmt.Errorf("block %d: invalid mining reward or miner", i)
		}
		if len(b.Txs) > MaxPendingTxs {
			return nil, fmt.Errorf("block %d: too many transactions", i)
		}
		l.height = int64(i)
		for _, tx := range b.Txs {
			if err := l.apply(tx); err != nil {
				return nil, fmt.Errorf("block %d: %w", i, err)
			}
		}
		if l.supply > math.MaxInt64-b.Reward || l.balances[b.Miner] > math.MaxInt64-b.Reward {
			return nil, fmt.Errorf("supply overflow")
		}
		l.supply += b.Reward
		l.balances[b.Miner] += b.Reward
	}
	if includePending {
		if len(s.Pending) > MaxPendingTxs {
			return nil, fmt.Errorf("mempool full")
		}
		l.height = int64(len(s.Blocks))
		for i, tx := range s.Pending {
			if err := l.apply(tx); err != nil {
				return nil, fmt.Errorf("pending transaction %d: %w", i, err)
			}
		}
	}
	return l, nil
}

func NewBlockchain() *Blockchain {
	return &Blockchain{state: chainState{ChainID: ChainID, Blocks: []*Block{GenesisBlock()}, Pending: []Transaction{}}}
}
func LoadOrCreate(path string) (*Blockchain, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		bc := NewBlockchain()
		bc.path = path
		if err := bc.persist(bc.state); err != nil {
			return nil, err
		}
		return bc, nil
	}
	if err != nil {
		return nil, err
	}
	var s chainState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("read ledger: %w", err)
	}
	if _, err := validateState(s, true); err != nil {
		return nil, fmt.Errorf("refusing invalid ledger: %w", err)
	}
	return &Blockchain{state: s, path: path}, nil
}

// persist is called while holding the write lock. Publish state only after a successful save.
func (bc *Blockchain) persist(s chainState) error {
	if bc.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(bc.path), ".gochain-*.tmp")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(temp, bc.path)
}
func copyState(s chainState) chainState {
	return chainState{ChainID: s.ChainID, Blocks: append([]*Block{}, s.Blocks...), Pending: append([]Transaction{}, s.Pending...)}
}
func cloneBlock(b *Block) *Block { c := *b; c.Txs = append([]Transaction{}, b.Txs...); return &c }
func (bc *Blockchain) Blocks() []*Block {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	out := make([]*Block, len(bc.state.Blocks))
	for i, b := range bc.state.Blocks {
		out[i] = cloneBlock(b)
	}
	return out
}
func (bc *Blockchain) AddTx(tx Transaction) error {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	if err := bc.checkCheckpoint(bc.state, true); err != nil {
		return err
	}
	s := copyState(bc.state)
	s.Pending = append(s.Pending, tx)
	if _, err := validateState(s, true); err != nil {
		return err
	}
	if err := bc.checkCheckpoint(s, true); err != nil {
		return err
	}
	if err := bc.persist(s); err != nil {
		return fmt.Errorf("save transaction: %w", err)
	}
	bc.state = s
	return nil
}
func (bc *Blockchain) MinePending(miner string) (*Block, error) {
	if !validAddress(miner) {
		return nil, fmt.Errorf("miner must be a GoChain wallet address")
	}
	bc.mu.Lock()
	defer bc.mu.Unlock()
	if err := bc.checkCheckpoint(bc.state, true); err != nil {
		return nil, err
	}
	if _, err := validateState(bc.state, true); err != nil {
		return nil, err
	}
	s := copyState(bc.state)
	b := NewBlock(len(s.Blocks), s.Blocks[len(s.Blocks)-1].Hash, s.Pending, miner)
	if b.Timestamp < s.Blocks[len(s.Blocks)-1].Timestamp {
		b.Timestamp = s.Blocks[len(s.Blocks)-1].Timestamp
	}
	if bc.checkpoint != nil && b.Timestamp < bc.checkpoint.IssuedAt {
		b.Timestamp = bc.checkpoint.IssuedAt
	}
	b.Reward = rewardAt(b.Timestamp)
	b.Mine(Difficulty)
	s.Blocks = append(s.Blocks, b)
	s.Pending = []Transaction{}
	if _, err := validateState(s, true); err != nil {
		return nil, err
	}
	// Proof of work may cross an expiry or calendar boundary after its initial
	// readiness check. Recheck before publishing the newly mined state.
	if err := bc.checkCheckpoint(s, true); err != nil {
		return nil, err
	}
	if err := bc.persist(s); err != nil {
		return nil, fmt.Errorf("save mined block: %w", err)
	}
	bc.state = s
	return cloneBlock(b), nil
}
func (bc *Blockchain) Validate() error {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if err := bc.checkCheckpoint(bc.state, false); err != nil {
		return err
	}
	_, err := validateState(bc.state, true)
	return err
}

type Account struct {
	Address     string `json:"address"`
	Balance     int64  `json:"balance_atoms"`
	BalanceGO   string `json:"balance"`
	Available   int64  `json:"available_balance_atoms"`
	AvailableGO string `json:"available_balance"`
	NextNonce   uint64 `json:"next_nonce"`
	Unit        string `json:"unit"`
}

func (bc *Blockchain) Account(addr string) (Account, error) {
	if !validAddress(addr) {
		return Account{}, fmt.Errorf("invalid wallet address")
	}
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	confirmed, err := validateState(bc.state, false)
	if err != nil {
		return Account{}, err
	}
	pending, err := validateState(bc.state, true)
	if err != nil {
		return Account{}, err
	}
	if pending.nonces[addr] == math.MaxUint64 {
		return Account{}, fmt.Errorf("account nonce exhausted")
	}
	return Account{Address: addr, Balance: confirmed.balances[addr], BalanceGO: formatAmount(confirmed.balances[addr]), Available: pending.balances[addr], AvailableGO: formatAmount(pending.balances[addr]), NextNonce: pending.nonces[addr] + 1, Unit: "GO"}, nil
}

type Stats struct {
	ChainID              string `json:"chain_id"`
	Height               int    `json:"height"`
	Blocks               int    `json:"blocks"`
	PendingTxs           int    `json:"pending_txs"`
	TotalTransferred     int64  `json:"total_transferred_atoms"`
	TransferredGO        string `json:"total_transferred"`
	TotalSupply          int64  `json:"total_supply_atoms"`
	SupplyGO             string `json:"total_supply"`
	MaxSupply            *int64 `json:"max_supply"`
	BlockReward          int64  `json:"block_reward_atoms"`
	RewardGO             string `json:"block_reward"`
	NextHalving          string `json:"next_halving"`
	TargetSeconds        int64  `json:"target_block_seconds"`
	DifficultyAdjustment bool   `json:"difficulty_adjustment"`
	TipHash              string `json:"tip_hash"`
	Difficulty           int    `json:"difficulty"`
	Unit                 string `json:"unit"`
	Valid                bool   `json:"valid"`
	CheckpointRequired   bool   `json:"checkpoint_required"`
	CheckpointHeight     int    `json:"checkpoint_height,omitempty"`
	CheckpointIssuedAt   int64  `json:"checkpoint_issued_at,omitempty"`
	CheckpointReady      bool   `json:"checkpoint_ready"`
}

func (bc *Blockchain) Stats() Stats {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	s := Stats{ChainID: ChainID, Height: len(bc.state.Blocks) - 1, Blocks: len(bc.state.Blocks), PendingTxs: len(bc.state.Pending), BlockReward: rewardAt(time.Now().Unix()), RewardGO: formatAmount(rewardAt(time.Now().Unix())), NextHalving: nextHalving(time.Now().Unix()), TargetSeconds: TargetBlockSeconds, TipHash: bc.state.Blocks[len(bc.state.Blocks)-1].Hash, Difficulty: Difficulty, Unit: "GO"}
	l, err := validateState(bc.state, false)
	if err == nil {
		s.TotalSupply = l.supply
		s.TotalTransferred = l.transferred
		s.SupplyGO = formatAmount(l.supply)
		s.TransferredGO = formatAmount(l.transferred)
	}
	_, pendingErr := validateState(bc.state, true)
	s.CheckpointRequired = CheckpointRequired
	s.CheckpointReady = bc.checkCheckpoint(bc.state, true) == nil
	if bc.checkpoint != nil {
		s.CheckpointHeight = bc.checkpoint.Height
		s.CheckpointIssuedAt = bc.checkpoint.IssuedAt
	}
	s.Valid = err == nil && pendingErr == nil && bc.checkCheckpoint(bc.state, false) == nil
	return s
}
