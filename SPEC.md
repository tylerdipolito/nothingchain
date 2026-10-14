# GoChain specification — minute/leap experiment v2

## Network and amounts

- Chain/signature domain: `gochain-minute-leap-v2`.
- Ledger: `gochain-v2.json`; original v1 ledger and wallet keys remain unchanged.
- One GO = 100,000,000 integer atomic units. No floating point in consensus.
- Transaction `amount_atoms` and block `reward_atoms` are int64 atomic amounts.
- Display fields are exact decimal GO strings; parallel `_atoms` fields are integers.

## Issuance

- Genesis reward is zero. Initial non-genesis reward is 1,000,000,000 atoms (10 GO).
- Count actual Gregorian February 29 cutoffs at 00:00 UTC, beginning in 2028,
  up to and including a block's timestamp. Right-shift the initial atomic reward
  by that count; after 64 halvings return zero explicitly. Rounding is downward.
- 2100, 2200 and 2300 have no leap-day halving; 2400 does.
- No premine, fees, independent hard cap, or supply import from earlier networks.
- Supply is the checked sum of validated rewards. Transfers conserve supply.
- Reward is credited after ordinary transactions in its block; it cannot fund a
  transaction in that same block. There is no additional maturity delay.

## Transactions and wallets

- Ed25519 signatures. Address: `go1` + lowercase SHA-256(public-key) hex.
- Signing JSON field order: chain, from, to, amount_atoms, memo, nonce.
- Public key must match sender. Signature covers every signing field and chain ID.
- Nonnegative amount, sufficient projected balance, sequential nonce starting at
  one, at most 512 memo bytes. Zero-amount signed messages remain allowed.
- Mempool projection reserves funds/nonces. Maximum 1,000 pending transfers.
- Wallet creation is exclusive; wallet load checks key/address consistency.
- Seeds are unencrypted on disk; CLI output contains public information only.

## Blocks and timestamps

- Hash field order: chain, index, timestamp, txs, prev_hash, nonce, miner,
  reward_atoms. Hash is SHA-256 of deterministic Go JSON.
- Work: four leading hexadecimal zeros, nonnegative nonce, no adjustment yet.
- Genesis is deterministic: timestamp 1728000000, index 0, prev_hash `0`, empty
  transactions, empty miner, zero reward, valid proof of work in the new domain.
- Contiguous indices, exact previous-hash link, nondecreasing timestamps, exact
  scheduled reward, valid miner address, at most 1,000 transactions per block.
- Receiver rejects any timestamp > local UTC time + 120 seconds. Historical
  validation cannot prove wall-clock creation time; backdated competing histories
  remain a limitation of this local calendar-based experiment.

## Mining cadence

- Target setting: 60 seconds; automatic mining uses a local timer (one block on
  startup, then the interval). This is not a network-wide consensus interval.
- Multiple miners or manual calls can accelerate issuance. Difficulty retargeting
  and an adversarial timestamp policy are not implemented in this experiment.

## Peer synchronization and fork choice

- Explicit loopback HTTP peers, maximum eight, polled every two seconds.
- GET /sync returns a snapshot: chain_id, blocks, pending.
- Response bound: 8 MiB; chain bound: 10,000 blocks; pending bound: 1,000.
- Verify every candidate block and transaction locally before selecting a chain.
- Greater block count wins because per-block required work is constant.
- Equal count: smaller tip hash wins. With future variable targets this rule must
  be replaced by cumulative-work comparison; it cannot be reused unchanged.
- Reorganizations rebuild balances/nonces from the selected chain. Merge pending
  and valid orphaned transfers, excluding replays, invalid entries and overspends.
  Retry nonce-dependent entries after predecessors; respect mempool capacity.
- Preserve old state if validation or disk persistence fails. Adopted data and
  exported snapshots are copied to avoid external mutation.
- No discovery, public peers, economic finality, or public-network security claim.

## Operation

- One writer process per ledger; mutex-protected reads, mining, and adoption.
- Save through a temporary file before publishing in-memory state.
- Different --data paths allow independent processes on one PC.
- --peers configures explicit peers; /peers reports sync health.
- Default bind: 127.0.0.1. No startup service installation.
