# Reviewer handoff

Repository: https://github.com/tylerdipolito/nothingchain
Branch: gochain-amm-review
Original baseline: abd9a86af75162e3dd3efc1e70d114e67261c576

## October 15 review follow-up

- Added a separate **trusted local** GO/Solana bridge prototype, with server-held
  dedicated wallets, eight-decimal wGO, lock/mint and finalized burn/release,
  synced signed intents, pending-operation exclusion, backing checks and recorded
  source/release anchors. Start `scripts/Start-GoBridge.ps1`; read `BRIDGE.md` before
  reviewing its trust and pruning assumptions. No public bridge, old-balance
  migration, external deposit watcher or extension-wallet bridge is claimed.
- `solana-devnet/scripts/verify-bridge.ts --isolated` exercises real lock/mint,
  lost-acknowledgement recovery, a separate pool/swap, burn/release and a changed
  anchor fixture. Core and engine unit tests cover precision, signing, durable
  recovery, failed/expired submissions, liabilities and pruning/rollback handling.

- Closed the faucet request-body race and added exclusive, synced per-wallet
  claim files. A real isolated process test stopped the server after durable
  claim creation/before funding, removed its summary entry, restarted and proved
  replay rejection with zero issuance. Run `npm run verify:faucet -- --isolated`.
- Client transactions now save recovery receipts before broadcast, distinguish
  cancellation/confirmed failure/uncertain outcome, and never automatically
  rebroadcast after confirmation loss. Unresolved receipts block another trade
  until historical status/expiry reconciliation. Unit tests cover those states;
  browser tests verified cancellation, confirmed swap, and receipt reload.
  The guard assumes one signing tab/tool per wallet, not global coordination.
- Solana integration now requires explicit `--isolated`: run
  `npm run verify -- --isolated` only against disposable local test state.

- Solana UI now supports encrypted local-wallet downloads, optional encrypted
  browser storage, reload/unlock and file restoration. Browser QA on an isolated
  validator proved the same address/balances after reload, wrong-password refusal,
  download restoration, and a confirmed swap signed by the restored wallet.
  Browser backups for a different wallet are not silently overwritten. This
  feature is localnet-only and does not make session keys production-safe.
- Start-GoPools.ps1 was exercised with both test services stopped: it resumed
  the existing isolated ledger and started Vite, then passed a repeat health
  check without replacing processes. It never resets or initializes a ledger.
- Solana integration now submits an actual failing deposit with preflight
  bypassed, verifies logs show successful first token CPI then failed second
  token CPI, and proves token balances/reserves/LP supply rolled back. The test
  network fee is still charged. The full integration suite passed on isolated
  RPC 8997, not the user's active pool.
- npm audit still reports 12 inherited advisories (7 moderate, 5 high); no forced
  downgrade was applied. This remains a local/testnet release, not approval to
  accept externally valuable assets or deploy publicly with the faucet enabled.

- Added the opt-in `checkpoint` build and CHECKPOINTS.md: a separate chain/signing
  domain, signed hash/time anchors, explicit operator public-key pinning, 24-hour
  expiry and halving-boundary freeze. Legacy builds stay unchanged. Test the
  `amm checkpoint` tag combination. Runtime checks on isolated port 18084 verified
  mining, signed transfer, checkpoint renewal, restart and supply replay. The
  authority is explicitly trusted; this does not fix fixed-difficulty economics
  or provide permissionless consensus. The legacy reproduction below remains
  applicable to the old networks.

- Reproduced a peer mempool expiry bug: a transaction expiring at the confirmed
  tip was rejected by local admission but retained by peer synchronization.
  Peer reconciliation now uses the next execution height, matching AddTx.
  Regression: TestAMMPeerRejectsExpiredPendingAtNextHeight.
- Reproduced legacy calendar timestamp grinding in an isolated in-memory test:
  at the same validation time, a three-block backdated history issues 30 GO
  while a two-block history crossing the first halving issues 15 GO. Existing
  fork choice prefers the longer history. This is NOT fixed by the mempool
  change. Regression/characterization: TestLegacyCalendarRewardTimestampGrinding.
  A safe redesign requires a new, explicitly versioned network and issuance
  policy; do not silently reinterpret existing ledgers. Median timestamps alone
  cannot prove elapsed calendar time or eliminate historical backdating.
- Executed successfully after the peer fix: both Go test variants, AMM Go vet,
  all six reward-helper Python tests, all three TypeScript tests, and the Vite
  production build. JS tooling still reports native bigint fallback and a
  punycode deprecation warning. These checks do not constitute a production audit.


Read README.md, SPEC.md, and AMM.md. Review the entire branch against the baseline,
then focus on simulation.go, amm_test.go, simulation_test.go, and scripts/ for the
latest work. Build and actually run both network variants using fresh test data.

```powershell
go test ./...
go test -tags amm ./...
go vet ./...
go vet -tags amm ./...
go test -race -tags amm ./...
python -m unittest discover -s scripts -p test_reward_liquidity.py -v
go build -tags amm -o gochain-sim.exe .
.\gochain-sim.exe simulate --seed 42 --attempts 100 --out simulation-local
```

Review supply conservation, LP ownership and dilution, overflow/rounding, signature
and nonce replay, slippage/expiry, mempool effects, fork adoption and orphan AMM
transactions. For the reward helper, test interrupted submissions, stale locks,
restart behavior, budget authorization, concurrent wallet use, and reorganizations.
For the simulator, verify independence from live ledgers, reproducibility, quote
versus execution checks, accounting and clearly stated limitations.

Known limits: fixed PoW difficulty and local one-minute timer, loopback-only peers,
timestamp-dependent calendar issuance, unencrypted wallet seeds, unbacked testUSD,
public deterministic simulator keys, single-threaded simulated trading, and no
production audit. The reward helper submits one bounded deposit per journal;
multiple journals authorize separate budgets. It is not a continuous reinvestment
service. Dynamic fees, privacy and post-quantum cryptography are not implemented.

Report confirmed findings by severity with reproducible steps, impact, code
locations, and recommended fixes. Distinguish test/model limitations from security
bugs. Do not change economics, deploy, or use real assets during the review.

## Independent Solana AMM prototype

The solana-devnet/ directory adds our own Rust/Anchor constant-product program
and a wallet-connected browser app. Read its README for build/deployment/test
commands and the exact public devnet funding blocker. The current verified
deployment is localnet, not public devnet. No Orca engine or SDK is used.

Prioritize pool/vault/LP mint authorities, atomic deposit/swap/withdraw behavior,
rounding and overflow, quote expiry, locked LP ownership, client integer parity,
browser signing, Vite private-file denial and faucet origin/journal checks.
Run Rust unit tests, npm test/build, and localnet integration verification.
Inherited npm advisories remain; concentrated liquidity and mainnet are out of
scope. Private deployer/program keys and runtime deployment JSON are excluded
from Git. Generate fresh test keys for your own local validator.
