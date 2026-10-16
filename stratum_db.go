package main

import (
	"database/sql"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// PayoutMode determines how GoChain mining rewards are distributed.
type PayoutMode string

const (
	PayoutModeHybrid PayoutMode = "hybrid" // 10% finder bonus, 90% difficulty-weighted PPLNS
	PayoutModeSolo   PayoutMode = "solo"   // 100% to the block finder
)

// WorkerRecord represents a registered miner's identity, stats, and balance.
type WorkerRecord struct {
	WorkerID        string  `json:"worker_id"`
	MinerGO         string  `json:"miner_go"`
	MinerBTC        string  `json:"miner_btc"`
	WorkerName      string  `json:"worker_name"`
	FirstSeenAt     int64   `json:"first_seen_at"`
	LastSeenAt      int64   `json:"last_seen_at"`
	SharesAccepted  int64   `json:"shares_accepted"`
	SharesRejected  int64   `json:"shares_rejected"`
	TotalDifficulty float64 `json:"total_difficulty"`
	EarnedGOAtoms   int64   `json:"earned_go_atoms"`
	EarnedGOFormatted string `json:"earned_go_formatted"`
	PaidGOAtoms     int64   `json:"paid_go_atoms"`
	EarnedBTCSat    int64   `json:"earned_btc_satoshis"`
	EarnedBTCFormatted string `json:"earned_btc_formatted"`
	Online          bool    `json:"online"`
	HashrateGHs     float64 `json:"hashrate_ghs"`
}

// BlockRewardDistribution details how a block's rewards were credited.
type BlockRewardDistribution struct {
	Height          int64               `json:"height"`
	Chain           string              `json:"chain"`
	Hash            string              `json:"hash"`
	FinderWorkerID  string              `json:"finder_worker_id"`
	RewardBaseUnits int64               `json:"reward_base_units"`
	PayoutMode      string              `json:"payout_mode"`
	FinderReward    int64               `json:"finder_reward"`
	SharedReward    int64               `json:"shared_reward"`
	WindowShares    int64               `json:"window_shares"`
	CreatedAt       int64               `json:"created_at"`
	Payouts         []WorkerPayoutEntry `json:"payouts"`
}

// WorkerPayoutEntry records a single credit to a miner.
type WorkerPayoutEntry struct {
	WorkerID    string `json:"worker_id"`
	MinerGO     string `json:"miner_go"`
	AmountAtoms int64  `json:"amount_atoms"`
	PayoutType  string `json:"payout_type"` // "finder_bonus" or "pplns_share"
}

// PoolDatabase manages transactional, restart-safe SQLite storage for the pool.
type PoolDatabase struct {
	dbPath string
	db     *sql.DB
	mu     sync.Mutex
}

// NewPoolDatabase initializes the SQLite database and executes schema migrations.
func NewPoolDatabase(dbPath string) (*PoolDatabase, error) {
	dir := filepath.Dir(dbPath)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	db.SetMaxOpenConns(1) // Avoid lock contention with WAL

	p := &PoolDatabase{
		dbPath: dbPath,
		db:     db,
	}

	if err := p.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to init schema: %w", err)
	}

	log.Printf("[DATABASE] 🗄️ SQLite pool accounting active at %s (WAL mode)", dbPath)
	return p, nil
}

func (p *PoolDatabase) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS workers (
		worker_id TEXT PRIMARY KEY,
		miner_go TEXT NOT NULL,
		miner_btc TEXT NOT NULL,
		worker_name TEXT NOT NULL,
		first_seen_at INTEGER NOT NULL,
		last_seen_at INTEGER NOT NULL,
		shares_accepted INTEGER NOT NULL DEFAULT 0,
		shares_rejected INTEGER NOT NULL DEFAULT 0,
		total_difficulty REAL NOT NULL DEFAULT 0.0,
		earned_go_atoms INTEGER NOT NULL DEFAULT 0,
		paid_go_atoms INTEGER NOT NULL DEFAULT 0,
		earned_btc_satoshis INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS shares (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		worker_id TEXT NOT NULL,
		job_id TEXT NOT NULL,
		extranonce1 TEXT NOT NULL,
		extranonce2 TEXT NOT NULL,
		nonce TEXT NOT NULL,
		difficulty REAL NOT NULL,
		hash TEXT NOT NULL,
		is_go_block INTEGER NOT NULL DEFAULT 0,
		is_btc_block INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		UNIQUE(job_id, extranonce1, extranonce2, nonce)
	);

	CREATE INDEX IF NOT EXISTS idx_shares_worker ON shares(worker_id);
	CREATE INDEX IF NOT EXISTS idx_shares_created ON shares(created_at);

	CREATE TABLE IF NOT EXISTS blocks (
		height INTEGER NOT NULL,
		chain TEXT NOT NULL,
		hash TEXT PRIMARY KEY,
		finder_worker_id TEXT NOT NULL,
		reward_base_units INTEGER NOT NULL,
		payout_mode TEXT NOT NULL,
		finder_reward INTEGER NOT NULL,
		shared_reward INTEGER NOT NULL,
		shares_window_count INTEGER NOT NULL,
		created_at INTEGER NOT NULL,
		confirmed INTEGER NOT NULL DEFAULT 1
	);

	CREATE TABLE IF NOT EXISTS payouts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		block_hash TEXT NOT NULL,
		worker_id TEXT NOT NULL,
		miner_go TEXT NOT NULL,
		amount_atoms INTEGER NOT NULL,
		payout_type TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		FOREIGN KEY(block_hash) REFERENCES blocks(hash)
	);
	`
	_, err := p.db.Exec(schema)
	return err
}

// Close gracefully closes the database connection.
func (p *PoolDatabase) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.db.Close()
}

// RegisterOrUpdateWorker ensures the worker identity exists in the database.
func (p *PoolDatabase) RegisterOrUpdateWorker(workerID, minerGO, minerBTC, workerName string) (*WorkerRecord, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now().Unix()
	query := `
	INSERT INTO workers (worker_id, miner_go, miner_btc, worker_name, first_seen_at, last_seen_at)
	VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT(worker_id) DO UPDATE SET
		last_seen_at = excluded.last_seen_at,
		miner_go = CASE WHEN excluded.miner_go != '' THEN excluded.miner_go ELSE workers.miner_go END,
		miner_btc = CASE WHEN excluded.miner_btc != '' THEN excluded.miner_btc ELSE workers.miner_btc END;
	`
	_, err := p.db.Exec(query, workerID, minerGO, minerBTC, workerName, now, now)
	if err != nil {
		return nil, err
	}

	return p.getWorkerLocked(workerID)
}

func (p *PoolDatabase) getWorkerLocked(workerID string) (*WorkerRecord, error) {
	row := p.db.QueryRow(`
		SELECT worker_id, miner_go, miner_btc, worker_name, first_seen_at, last_seen_at,
		       shares_accepted, shares_rejected, total_difficulty, earned_go_atoms,
		       paid_go_atoms, earned_btc_satoshis
		FROM workers WHERE worker_id = ?`, workerID)

	var w WorkerRecord
	err := row.Scan(&w.WorkerID, &w.MinerGO, &w.MinerBTC, &w.WorkerName, &w.FirstSeenAt,
		&w.LastSeenAt, &w.SharesAccepted, &w.SharesRejected, &w.TotalDifficulty,
		&w.EarnedGOAtoms, &w.PaidGOAtoms, &w.EarnedBTCSat)
	if err != nil {
		return nil, err
	}
	w.EarnedGOFormatted = fmt.Sprintf("%.8f GO", float64(w.EarnedGOAtoms)/1e8)
	w.EarnedBTCFormatted = fmt.Sprintf("%.8f BTC", float64(w.EarnedBTCSat)/1e8)
	return &w, nil
}

// RecordShare attempts to insert a share into the database.
// Returns (accepted bool, isDuplicate bool, err error).
func (p *PoolDatabase) RecordShare(workerID, jobID, en1, en2, nonce string, diff float64, hash string, isGoBlock, isBtcBlock bool) (bool, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	tx, err := p.db.Begin()
	if err != nil {
		return false, false, err
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	goFlag := 0
	if isGoBlock {
		goFlag = 1
	}
	btcFlag := 0
	if isBtcBlock {
		btcFlag = 1
	}

	insertShare := `
	INSERT INTO shares (worker_id, job_id, extranonce1, extranonce2, nonce, difficulty, hash, is_go_block, is_btc_block, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err = tx.Exec(insertShare, workerID, jobID, en1, en2, nonce, diff, hash, goFlag, btcFlag, now)
	if err != nil {
		// Unique constraint violation means duplicate share
		if isUniqueConstraintErr(err) {
			_, _ = tx.Exec(`UPDATE workers SET shares_rejected = shares_rejected + 1, last_seen_at = ? WHERE worker_id = ?`, now, workerID)
			_ = tx.Commit()
			return false, true, nil
		}
		return false, false, err
	}

	updateWorker := `
	UPDATE workers SET
		shares_accepted = shares_accepted + 1,
		total_difficulty = total_difficulty + ?,
		last_seen_at = ?
	WHERE worker_id = ?
	`
	_, err = tx.Exec(updateWorker, diff, now, workerID)
	if err != nil {
		return false, false, err
	}

	if err := tx.Commit(); err != nil {
		return false, false, err
	}
	return true, false, nil
}

// RecordRejectedShare records an invalid or stale share without adding difficulty.
func (p *PoolDatabase) RecordRejectedShare(workerID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now().Unix()
	_, err := p.db.Exec(`UPDATE workers SET shares_rejected = shares_rejected + 1, last_seen_at = ? WHERE worker_id = ?`, now, workerID)
	return err
}

// DistributeGoBlockReward executes an atomic PPLNS or solo reward distribution for an accepted GoChain AuxPoW block.
func (p *PoolDatabase) DistributeGoBlockReward(height int64, blockHash, finderWorkerID string, rewardAtoms int64, mode PayoutMode, pplnsWindowShares int) (*BlockRewardDistribution, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	tx, err := p.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now().Unix()

	// Get finder record
	var finderGO string
	err = tx.QueryRow(`SELECT miner_go FROM workers WHERE worker_id = ?`, finderWorkerID).Scan(&finderGO)
	if err != nil {
		finderGO = finderWorkerID
	}

	var finderReward int64
	var sharedReward int64
	var payouts []WorkerPayoutEntry

	if mode == PayoutModeSolo {
		finderReward = rewardAtoms
		sharedReward = 0
		payouts = append(payouts, WorkerPayoutEntry{
			WorkerID:    finderWorkerID,
			MinerGO:     finderGO,
			AmountAtoms: finderReward,
			PayoutType:  "solo_finder",
		})
	} else {
		// Hybrid Mode: 10% Finder Bonus, 90% Difficulty-Weighted PPLNS
		finderBonus := int64(math.Round(float64(rewardAtoms) * 0.10)) // 10% = 100,000,000 atoms (1 GO)
		sharedPool := rewardAtoms - finderBonus                       // 90% = 900,000,000 atoms (9 GO)

		finderReward = finderBonus
		sharedReward = sharedPool

		// Fetch last N shares for PPLNS calculation
		rows, err := tx.Query(`
			SELECT worker_id, difficulty
			FROM shares
			ORDER BY id DESC
			LIMIT ?`, pplnsWindowShares)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch shares for PPLNS: %w", err)
		}
		defer rows.Close()

		workerWork := make(map[string]float64)
		var totalWork float64
		var windowCount int64

		for rows.Next() {
			var wid string
			var diff float64
			if err := rows.Scan(&wid, &diff); err == nil {
				workerWork[wid] += diff
				totalWork += diff
				windowCount++
			}
		}

		// First credit the finder bonus
		payouts = append(payouts, WorkerPayoutEntry{
			WorkerID:    finderWorkerID,
			MinerGO:     finderGO,
			AmountAtoms: finderBonus,
			PayoutType:  "finder_bonus",
		})

		if totalWork > 0 && len(workerWork) > 0 {
			var distributedShared int64
			for wid, work := range workerWork {
				// Integer math with round down
				portion := int64(math.Floor(float64(sharedPool) * (work / totalWork)))
				if portion > 0 {
					var wGO string
					_ = tx.QueryRow(`SELECT miner_go FROM workers WHERE worker_id = ?`, wid).Scan(&wGO)
					if wGO == "" {
						wGO = wid
					}
					payouts = append(payouts, WorkerPayoutEntry{
						WorkerID:    wid,
						MinerGO:     wGO,
						AmountAtoms: portion,
						PayoutType:  "pplns_share",
					})
					distributedShared += portion
				}
			}

			// Credit remainder dust to finder
			dust := sharedPool - distributedShared
			if dust > 0 {
				payouts[0].AmountAtoms += dust
				finderReward += dust
			}
		} else {
			// If no prior shares in window, entire reward goes to finder
			payouts[0].AmountAtoms = rewardAtoms
			finderReward = rewardAtoms
			sharedReward = 0
		}
	}

	// Record block in database
	insertBlock := `
	INSERT INTO blocks (height, chain, hash, finder_worker_id, reward_base_units, payout_mode, finder_reward, shared_reward, shares_window_count, created_at)
	VALUES (?, 'gochain', ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err = tx.Exec(insertBlock, height, blockHash, finderWorkerID, rewardAtoms, string(mode), finderReward, sharedReward, len(payouts), now)
	if err != nil {
		return nil, fmt.Errorf("failed to record block: %w", err)
	}

	// Record payouts and update worker balances
	var totalCredited int64
	for _, entry := range payouts {
		_, err = tx.Exec(`
			INSERT INTO payouts (block_hash, worker_id, miner_go, amount_atoms, payout_type, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			blockHash, entry.WorkerID, entry.MinerGO, entry.AmountAtoms, entry.PayoutType, now)
		if err != nil {
			return nil, fmt.Errorf("failed to insert payout: %w", err)
		}

		_, err = tx.Exec(`
			UPDATE workers SET earned_go_atoms = earned_go_atoms + ? WHERE worker_id = ?`,
			entry.AmountAtoms, entry.WorkerID)
		if err != nil {
			return nil, fmt.Errorf("failed to credit worker balance: %w", err)
		}
		totalCredited += entry.AmountAtoms
	}

	// Invariant check: Total credited must equal EXACTLY rewardAtoms (10 GO)
	if totalCredited != rewardAtoms {
		return nil, fmt.Errorf("CRITICAL ACCOUNTING ERROR: total credited %d != on-chain block reward %d", totalCredited, rewardAtoms)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	dist := &BlockRewardDistribution{
		Height:          height,
		Chain:           "gochain",
		Hash:            blockHash,
		FinderWorkerID:  finderWorkerID,
		RewardBaseUnits: rewardAtoms,
		PayoutMode:      string(mode),
		FinderReward:    finderReward,
		SharedReward:    sharedReward,
		WindowShares:    int64(len(payouts)),
		CreatedAt:       now,
		Payouts:         payouts,
	}

	log.Printf("[REWARD] 💰 GoChain Block #%d (%s) distributed! Total: %d atoms (%s). Finder (%s): %d atoms. Payouts: %d",
		height, mode, rewardAtoms, fmt.Sprintf("%.8f GO", float64(rewardAtoms)/1e8),
		finderWorkerID, finderReward, len(payouts))

	return dist, nil
}

// RecordBtcBlockPayout credits the full Bitcoin parent block reward to the finder.
func (p *PoolDatabase) RecordBtcBlockPayout(height int64, blockHash, finderWorkerID string, rewardSatoshis int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	_, err = tx.Exec(`
		INSERT INTO blocks (height, chain, hash, finder_worker_id, reward_base_units, payout_mode, finder_reward, shared_reward, shares_window_count, created_at)
		VALUES (?, 'bitcoin', ?, ?, ?, 'solo_lottery', ?, 0, 1, ?)`,
		height, blockHash, finderWorkerID, rewardSatoshis, rewardSatoshis, now)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`
		UPDATE workers SET earned_btc_satoshis = earned_btc_satoshis + ? WHERE worker_id = ?`,
		rewardSatoshis, finderWorkerID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// GetAllWorkers returns all registered workers and their current database statistics.
func (p *PoolDatabase) GetAllWorkers() ([]WorkerRecord, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	rows, err := p.db.Query(`
		SELECT worker_id, miner_go, miner_btc, worker_name, first_seen_at, last_seen_at,
		       shares_accepted, shares_rejected, total_difficulty, earned_go_atoms,
		       paid_go_atoms, earned_btc_satoshis
		FROM workers ORDER BY last_seen_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []WorkerRecord
	for rows.Next() {
		var w WorkerRecord
		if err := rows.Scan(&w.WorkerID, &w.MinerGO, &w.MinerBTC, &w.WorkerName, &w.FirstSeenAt,
			&w.LastSeenAt, &w.SharesAccepted, &w.SharesRejected, &w.TotalDifficulty,
			&w.EarnedGOAtoms, &w.PaidGOAtoms, &w.EarnedBTCSat); err == nil {
			w.EarnedGOFormatted = fmt.Sprintf("%.8f GO", float64(w.EarnedGOAtoms)/1e8)
			w.EarnedBTCFormatted = fmt.Sprintf("%.8f BTC", float64(w.EarnedBTCSat)/1e8)
			list = append(list, w)
		}
	}
	return list, nil
}

// GetRecentPayouts returns the latest block payout distributions.
func (p *PoolDatabase) GetRecentPayouts(limit int) ([]BlockRewardDistribution, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	rows, err := p.db.Query(`
		SELECT height, chain, hash, finder_worker_id, reward_base_units, payout_mode,
		       finder_reward, shared_reward, shares_window_count, created_at
		FROM blocks WHERE chain = 'gochain'
		ORDER BY height DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []BlockRewardDistribution
	for rows.Next() {
		var b BlockRewardDistribution
		if err := rows.Scan(&b.Height, &b.Chain, &b.Hash, &b.FinderWorkerID, &b.RewardBaseUnits,
			&b.PayoutMode, &b.FinderReward, &b.SharedReward, &b.WindowShares, &b.CreatedAt); err == nil {
			list = append(list, b)
		}
	}
	return list, nil
}

func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return containsAny(s, "UNIQUE constraint failed", "constraint failed", "PRIMARY KEY")
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
