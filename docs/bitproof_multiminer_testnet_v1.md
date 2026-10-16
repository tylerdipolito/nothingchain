# Bitproof Multi-Miner Testnet v1: Architecture & Audit Report

**Audit Date:** October 15, 2026  
**Status:** Live & Verified on Testnet  
**Components:** Bitproof Multi-Miner Stratum Coordinator, SQLite Transactional Accounting Engine (`bitproof_pool.db`), Bitcoin Core v28.0 (`regtest`), GoChain AuxPoW Testnet (`ledger-auxpow-test.json`), Physical Bitaxe BM1370 ASIC + Concurrent Simulated Miners

---

## Executive Summary

Bitproof has successfully transitioned from single-miner solo operation to the **Multi-Miner Testnet v1** architecture. This milestone implements:
1. **Configurable Payout Engine**: Defaulting to the Parasite-style **Hybrid Model** (10% Finder Bonus, 90% difficulty-weighted PPLNS), with alternative support for 100% solo mining.
2. **ACID SQLite Accounting**: Migrated from ephemeral in-memory storage to a transactional SQLite database (`bitproof_pool.db` in WAL mode). Worker identities, share records, block confirmations, and balances persist across server restarts without double counting.
3. **Integer Base Unit Conservation**: All GO mining rewards are calculated in integer base units ($1\text{ GO} = 100,000,000\text{ atoms}$). Under no circumstance can the pool credit more or less than the on-chain confirmed reward ($1,000,000,000\text{ atoms}$ per block).
4. **Concurrent Multi-Miner Support**: Successfully validated with one physical Bitaxe BM1370 ASIC hashing at ~1.15 TH/s alongside multiple concurrent simulated miners with disjoint `extranonce1` search spaces.
5. **Robust Error Handling**: Verified rejection of duplicate shares (Error 22), stale jobs (Error 21), and low-difficulty submissions (Error 23).

---

## 1. Payout Models & Mathematical Conservation

The coordinator supports two selectable payout distribution modes via `--payout-mode`:

```mermaid
pie title GoChain Block Reward Distribution (Hybrid Mode)
    "Shared PPLNS Pool (90%)" : 900000000
    "Finder Bonus (10%)" : 100000000
```

### Hybrid Model (Default)
When an AuxPoW block is validated on GoChain, the $10\text{ GO}$ ($1,000,000,000\text{ atoms}$) block reward is split:
* **Finder Bonus ($10\%$):** Exactly $100,000,000\text{ atoms}$ ($1\text{ GO}$) credited to the worker who found the block.
* **Shared PPLNS Pool ($90\%$):** Exactly $900,000,000\text{ atoms}$ ($9\text{ GO}$) distributed among active miners proportional to their contributed difficulty in the last $N$ shares window ($N = 1000$ default):
  $$\text{Reward}_k = \left\lfloor \frac{900,000,000 \times W_k}{W_{\text{total}}} \right\rfloor$$
  where $W_k = \sum_{i \in \text{window}_k} \text{Diff}_i$ and $W_{\text{total}} = \sum_{k} W_k$.
* **Conservation Invariant:** Integer division dust is credited to the finder. The sum of all payouts is mathematically guaranteed to equal $1,000,000,000\text{ atoms}$.

### Solo Model
100% of the block reward ($1,000,000,000\text{ atoms}$) is credited directly to the finder.

---

## 2. Database Schema (`bitproof_pool.db`)

The SQLite database operates in Write-Ahead Logging (`WAL`) mode with full ACID transaction guarantees:

```sql
CREATE TABLE workers (
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

CREATE TABLE shares (
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
    UNIQUE(job_id, extranonce1, extranonce2, nonce) -- Database-enforced duplicate prevention
);

CREATE TABLE blocks (
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

CREATE TABLE payouts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    block_hash TEXT NOT NULL,
    worker_id TEXT NOT NULL,
    miner_go TEXT NOT NULL,
    amount_atoms INTEGER NOT NULL,
    payout_type TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    FOREIGN KEY(block_hash) REFERENCES blocks(hash)
);
```

---

## 3. Integration & Concurrency Test Results

### Unit Tests (`stratum_db_test.go`)
* `TestPoolDatabasePPLNSDistribution`: 3 workers with varying difficulties (Alpha 20% work, Beta 40% work, Gamma 40% work).
  * Finder (Alpha): Credited $100\text{M} + 180\text{M} = 280,000,000\text{ atoms}$ ($2.80\text{ GO}$).
  * Beta: Credited $360,000,000\text{ atoms}$ ($3.60\text{ GO}$).
  * Gamma: Credited $360,000,000\text{ atoms}$ ($3.60\text{ GO}$).
  * **Result: PASSED** ($280\text{M} + 360\text{M} + 360\text{M} = 1,000,000,000\text{ atoms}$).
* `TestPoolDatabaseRestartPersistence`: Verified that database restart leaves all worker balances and shares 100% intact.
  * **Result: PASSED**.

### Live Protocol Concurrency Tests (`test_multiminer_suite.py` & `test_complete_audit.py`)
* **Extranonce Disjoint Space:**
  * Worker 1 (Bitaxe BM1370): `extranonce1 = 00000001`
  * Worker 2 (Simulated Alpha): `extranonce1 = 00000002`
  * Worker 3 (Simulated Beta): `extranonce1 = 00000003`
  * Worker 4 (Audit Bot): `extranonce1 = 00000004`
  * Result: **Zero keyspace overlap across workers**.
* **Duplicate Share Rejection:**
  * First submission: `result: true`
  * Immediate re-submission: `result: false, error: [22, "Duplicate share", null]`
  * Result: **100% Duplicate Prevention verified**.
* **Stale Job Rejection:**
  * Submission with expired job ID: `result: false, error: [21, "Job not found or stale", null]`.

---

## 4. Live Multi-Miner Accounting Snapshot (`GET /api/workers`)

Active workers registered in `bitproof_pool.db` during testnet hashing:

| Worker ID | Friendly Name | Status | Contributed Work | Acc / Rej | GO Balance |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `go19c5...2272.bitaxe` | **bitaxe** | **ONLINE (~1.15 TH/s)** | 768.0 diff | 48 / 0 | `0.00000000 GO` |
| `go1_simulated_alpha...` | **sim_miner_1** | IDLE | 16.0 diff | 1 / 0 | `0.00000000 GO` |
| `go1_simulated_beta...` | **sim_miner_2** | IDLE | 0.0 diff | 0 / 0 | `0.00000000 GO` |
| `go1_audit_tester...` | **audit_bot** | IDLE | 16.0 diff | 1 / 0 | `0.00000000 GO` |

---

## 5. Technical Proposal: Verifiable Hardware Identity vs. Firmware Claims

### The Core Vulnerability of Firmware Claims
Any client-side user-agent, Stratum version string, or AxeOS banner (e.g. `"ESP-Miner/v2.14.0"`) is trivial for an attacker to spoof. A single Python script or modified Antminer S21 firmware can send identical authorization strings while pumping 200 TH/s of industrial hashrate into the pool.

### Architectural Recommendation
1. **Never Depend On Firmware Strings in Consensus:**  
   The GoChain blockchain consensus rules must remain **100% hardware-agnostic**. Blocks are validated strictly on valid SHA-256d AuxPoW mathematics. Making the consensus layer verify hardware claims creates brittle, easily gamed rules.
2. **Pool-Layer Anti-Whale Protections:**
   * **Hashrate Band Capping:** The pool can enforce that workers claiming Bitaxe status must hash below a maximum ceiling (e.g. $\le 2.0\text{ TH/s}$). Any worker exceeding this hashrate is automatically categorized as standard ASIC.
   * **Hardware Nonce / Timing Dispersion:** BM1370 chips have specific nonce turnaround timings and ASIC-boost frequency profiles that differ from multi-hashboard industrial rigs.
   * **Stratum V2 & Secure Enclaves:** The ESP32-S3 microcontroller on the Bitaxe possesses factory-burned eFuse identifiers and hardware crypto accelerators. In future iterations, Stratum V2 Noise Protocol handshakes can require digital signatures from keys bound to open-source hardware registry certificates.
