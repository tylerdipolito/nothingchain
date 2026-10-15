# Local trusted GO → Solana bridge prototype

This is an operator-controlled local laboratory, not a trustless bridge or public
deployment. It connects a **separate checkpoint GoChain test ledger** on port 8086
to a **new eight-decimal SPL wGO mint** on the local Solana validator (8897).
Existing GoChain ledgers, wallets, Demo GO mint, and the six-decimal Demo GO AMM
are not migrated. wGO in this prototype represents GO mined on the new checkpoint
ledger, not the user's old 8082 chain balance. There is no public devnet/mainnet
bridge, external deposit watcher, extension-wallet bridge, multisig or light client.

## Run on Windows

Keep the existing local Solana validator running and our `goamm` program deployed
there (see `solana-devnet/README.md`). From the repository root:

```powershell
.\scripts\Start-GoBridge.ps1
```

Requires Go on PATH for the initial checkpoint binary build and Node/npm dependencies
installed with `npm ci` in `solana-devnet`. The launcher creates fresh dedicated
test keys on first use, starts/reuses its checkpoint node and bridge UI, and opens
**http://127.0.0.1:5180/**. It never resets an existing ledger, starts Solana, renews
a checkpoint automatically, or imports old wallets. `-NoBrowser` leaves it hidden.
`-NodePort`, `-Port`, and `-SolanaRpc` configure initial local ports; existing bridge
configuration must continue using the same endpoints and ledgers. Do not point
another operator at the same custody/mint with a different journal.

The page explicitly uses server-held dedicated test wallets, not Phantom. It has:

- balances, custody/supply/withdrawal accounting, identities and operation receipts;
- manual mining, GO deposits, wGO burns, and recovery;
- a separate backed wGO/testUSD pool, one-time seed and swaps with minimum output.

First mine a block, lock 10 GO, then mine three blocks. The mint occurs only after
the deposit has three confirmations (inclusion plus two descendants). Seed 5 wGO
and 500 testUSD, get a quote and swap. To return GO, burn some wallet wGO, wait for
Solana finality, then mine three GoChain blocks to finish the release. Unfinished
operations disable another trade. The bridge does not mine in the background.

CLI equivalent, from `solana-devnet`:

```powershell
npm run bridge -- status
npm run bridge -- mine 1
npm run bridge -- wrap 10
npm run bridge -- mine 3
npm run bridge -- seed-pool 5 500
npm run bridge -- quote testUSD 10
# Replace the last argument with the minimum returned by your fresh quote.
npm run bridge -- swap testUSD 10 0.09677333
npm run bridge -- unwrap 1
npm run bridge -- mine 3
npm run bridge -- recover
```

`wrap` returns a pending receipt, not immediate issuance. Never repeat wrap/unwrap
to recover a previous request: recover its existing journal operation. A repeated
wrap after completion is a **new, separately funded deposit**, not a replay.

## What enforces backing

- Dedicated GO custody wallet; the operator can spend it and is explicitly trusted.
- Separate wGO mint with **eight decimals**, the same as GO, no rounding/dust loss.
  No wGO faucet. Only test SOL for fees and a distinct unbacked eight-decimal
  testUSD mint are provisioned at initialization.
- A signed GO deposit must appear in the pinned node's validated checkpoint chain
  with three confirmations before minting. Its height/hash is recorded; removal
  of a recorded source or release anchor halts subsequent bridge actions.
- SPL wGO burn must be observed **finalized**, including successful transaction
  status, before a signed GO release is created. The burn transaction's signed
  memo binds operation ID, chain and the dedicated GO destination. Only burns
  created by this operator workflow are serviced; arbitrary burns are not claims.
- Each signed GO payment and Solana transaction is persisted before broadcast.
  Recovery reuses the same signature/nonce or exact signed bytes; it never creates
  a replacement payment or mint automatically. Confirmed failures, expired unknown
  Solana submissions, missing history and nonce conflicts require reconciliation.
- One exclusive directory lock serializes all CLI/UI actions. A second pending
  operation is refused. This is not distributed locking or cross-machine failover.
- Accounting requires custody >= actual SPL mint supply + finalized burns still
  awaiting GO release. A release already included but awaiting three confirmations
  is reported separately instead of counting its debit and liability twice.
  A projected mint also requires enough unallocated backing. Donations are surplus,
  not permission to issue a matching user deposit twice.

The bridge trusts the configured loopback GoChain RPC's validation/checkpoint
readiness and Solana RPC's finalized responses; it does not independently verify
a light-client proof or authenticate the Go checkpoint signing key. Genesis pins
detect Solana ledger changes; GoChain's fixed genesis is shared by fresh ledgers,
so recorded block anchors, local process ownership and the trusted node are material.
Three confirmations are not irreversible Go finality. If a previously bridged
deposit is removed, **halting does not undo already circulated wGO**. An operator
must resolve the deficit before resuming. The Go checkpoint authority and AMM
program upgrade authority retain their existing trust powers.

### Pruned Solana history

The local validator prunes old blocks. After successfully observing finality the
journal fsyncs the finalized slot. Later, a missing signature is accepted **only**
when that exact slot is older than the RPC's first available block and there is a
prior durable finalized receipt. Otherwise missing or contradictory history halts.
An unresolved signed transaction receives no pruning exemption and cannot justify
a GO release. This relies on Solana finality and trusted local receipts; it cannot
prove safety against an operator rolling back both journal and validator snapshots.
Keep archival RPC/history for stronger operational recovery. A validator rollback
behind a recorded finalized slot is rejected; a malicious history reporting RPC
is outside this prototype's trust model.

## Runtime files and recovery

Ignored, private local state:

- `solana-devnet/.local/bridge/config.json`: public identities and endpoint pins;
- `keys.json`: **unencrypted private custody, mint and dedicated user keys**;
- `journal.jsonl`: fsynced append-only snapshots, signed intents and finalized receipts;
- `operator.lock`: process ownership; a stale file is never automatically removed;
- `solana-devnet/.local/bridge-node/`: binary, ledger, dedicated checkpoint authority,
  certificate and logs. Do not publish or expose this directory.

Back up keys, full journal and both ledgers consistently while stopped. A checksum
detects accidental log corruption; it is not authentication, anti-rollback storage
or a substitute for backups. Torn logs and missing setup history fail closed.
Do not delete runtime files or edit balances to get past an error. After a crash,
verify no old bridge process remains before removing its stale operator lock, then
run `recover`. No claim is made about physical power-loss durability on every OS.

The UI binds only to 127.0.0.1, requires exact Host/Origin and a random server-session
request token, and serves no filesystem/key routes. This limits browser cross-origin
requests; **it is not user authentication or protection against other local programs**.
It must not be reverse-proxied, exposed publicly, or used with real assets.

Checkpoint readiness expires after 24 hours or a halving boundary. Stop only this
prototype's GoChain node cleanly, inspect the ledger, sign a new checkpoint with
its dedicated authority and restart with that new file, following `CHECKPOINTS.md`.
Do not reset genesis or overwrite the certificate while its process is running.
Update the launcher's `checkpoint.json` only as an explicit inspected operator
renewal. The bridge has no renewal daemon or liveness guarantee.

## Verification

```powershell
cd solana-devnet
npm test
npm run build
npm run verify:bridge -- --isolated
```

The integration command requires a dedicated ready checkpoint node (default8086)
and local Solana validator with our program. It creates **fresh** wallets/mints
under `.local/bridge-verification-TIMESTAMP`, mines real blocks, locks/mints
10.00000001 GO/wGO, simulates a lost completion acknowledgement and recovers the
original mint signature without issuance, seeds a pool, swaps 10 testUSD for exactly
0.09775084 wGO, burns/releases 1.00000001 GO, tests the 1+2 confirmation path and
rejects a changed source anchor. Its final custody and supply must both be exactly
9 GO/wGO. The changed anchor is confined to the disposable journal, not a live
chain fork. The harness does not kill the validator or simulate a real power loss.
Reports are written inside its ignored runtime directory.

Unit tests additionally check exact signing/domain binding, precision limits,
confirmation depth, liabilities, concurrent operators, torn logs, unchanged signed
bytes after restart, failure/expiry handling and pruning/rollback decisions.
Existing AMM/program conservation tests remain separate. This is not an audit.

During initial implementation the earlier scratch `.local/bridge` lacked finalized
slot receipts and correctly halted once RPC history was pruned. It was preserved
as `.local/bridge-initial-pruned`; no keys/ledger were deleted or silently repaired.
The delivered UI uses a new dedicated mint/journal with the pruning-aware receipts.

Delivered runtime check: custody 9 GO, outstanding supply 9 wGO, no pending
withdrawals; pool reserves 4.90224916 wGO / 510 testUSD. The dedicated user has
61 GO (including mining rewards), 4.09775084 wGO and 9,490 testUSD. These are
point-in-time local results, not seeded production balances. The full suite had
20 passing tests and the TypeScript/Vite build passed. HTTP checks rejected wrong
origin/token, unsupported commands and private file routes, and executed the same
quoted swap successfully. Subsequent browser verification passed cancellation,
liquidity seed, quote/swap, reload during submission, recovery, GO lock/mint,
wGO burn/GO release and manual mining on disposable wallets. The isolated browser
round trip ended with custody and supply both 9 and no pending withdrawals.

## Pool candlesticks

The bridge pool view includes a reserve-ratio spot price, 1-minute / 5-minute /
1-hour candlesticks, testUSD volume bars and recent trades. Candle prices are
testUSD per wGO, derived from the gross execution amounts (including the input
fee), not a real dollar value or the marginal reserve-ratio price. OHLC compares
integer ratios and uses finalized chain slot/transaction ordering; floating-point
conversion is limited to the display. Hover/focus a candle for exact displayed
OHLC and atomic-volume-derived amounts. Times are UTC.

The collector verifies successful finalized direct swaps of this specific pool,
the program/discriminator, vault identities, eight-decimal balance changes and
signed input/minimum. Deposits, quotes, mining, failures and ambiguous multi-action
transactions do not become trades. A local synced `trades.jsonl` cache retains
captured trades after refresh/restart and RPC pruning, scoped to genesis and pool.
This is a display cache, never consensus state or a price oracle. Cache corruption
or RPC errors surface as chart errors and do not authorize or block asset issuance.

The collector scans the latest 100 finalized pool transactions, up to 25 unseen
transactions per refresh. Refresh again if a backlog is shown. It is not an
archival indexer: transactions already pruned before capture and activity outside
that bounded window may be missing. The chart labels this limitation, shows no
synthetic candles for empty intervals, and displays the last 120 traded intervals
and 20 recent trades. Sparse trading produces a sparse chart. No transaction is
submitted just to populate the user's chart.

## Request identity and interrupted operations

HTTP wrap, unwrap, pool seed and swap now require a request ID. The operation stores
that ID and a SHA-256 fingerprint of its command and exact argument strings before
broadcast. Replaying the same ID/parameters recovers the original operation, even
after completion or server restart; changing its parameters is rejected. This
serializes through the existing directory lock, not a second request database.
Different IDs are different authorized transactions, not automatically deduplicated.
Manual test mining is excluded: every mining click requests more test blocks.

The UI saves the request in localStorage **before** sending and retains it through
reloads, lost responses and uncertain errors. Recover resubmits that saved ID and
parameters. A completed journal receipt clears it. A validation error can clear it
only when the server locks and reads a valid journal proving no intent was saved.
Storage failures stop submission. Web Locks prevent two tabs from preparing new
requests simultaneously; another unresolved saved request blocks a new trade.
The slot is scoped to the mint and browser origin. Clearing browser storage or
using another browser can lose that client receipt; inspect the server journal.
This is not cross-machine coordination or protection against malicious local code.

CLI callers can pass `--request-id UUID` and must retain/reuse that ID themselves.
Without it, an explicit CLI invocation is a new transaction. Amount strings are
fingerprinted exactly: `1` and `1.0` with one ID are a parameter conflict.

Additional reproducible tests:

```powershell
cd solana-devnet
npm run verify:bridge-crashes -- --isolated
# Use the directory printed by that run, after completing any manual pending actions:
npm run verify:bridge-http -- --isolated .local/bridge-crash-TIMESTAMP
cd ..
$env:GO_BRIDGE_REORG = '1'
go test -tags "amm checkpoint" -run '^TestBridgeRealReorganization$' -v .
Remove-Item Env:GO_BRIDGE_REORG
```

The crash suite kills only its owned child at four real boundaries: saved deposit
before submission, mint after broadcast, burn after broadcast and GO release after
submission. It verifies the terminated PID before removing that child's stale lock.
Recovery, same-ID replay and conflicting parameters are checked against real chains;
exactly one release is included and custody/supply both end at 9. Production has no
fault-injection switch or automatic stale-lock removal. This is process-crash
coverage, not a physical power-loss test.

The opt-in Go test owns a separate in-memory node and genuinely adopts a longer,
valid fork that removes a confirmed bridged deposit. The bridge then refuses status,
wrap and unwrap without further issuance/payment. Existing wGO stays circulating
and becomes unbacked: halting is detection/containment, not automatic restitution.
Its reorg trigger is a test-only HTTP handler, absent from production. Ordinary
test runs skip this cross-chain test unless explicitly enabled. Neither suite resets
the user's ledgers or existing mint; all test keys/assets/reports remain ignored.
