# Local/testnet release guide

This release is for local experimentation with GoChain and the independent
Solana AMM. It does not establish public-network security, external asset
backing, a dollar peg, a production audit or a GoChain/Solana bridge.

## Run the Solana interface on this PC

From `C:\Projects\nothingchain`, after the README's first-time toolchain,
local-validator, deployment and dependency setup:

```powershell
.\scripts\Start-GoPools.ps1
```

It opens http://127.0.0.1:5174/ and reuses healthy existing services. If the
validator is stopped, provide its existing WSL ledger explicitly:

```powershell
.\scripts\Start-GoPools.ps1 -StartValidator -ValidatorLedger /absolute/path/to/existing-ledger
```

The launcher never creates/replaces a validator ledger, resets accounts, deploys
a program, generates a wallet, kills another service or mines GoChain blocks.
`-CheckOnly` verifies health and configured deployment without starting anything.
See `solana-devnet/README.md` for alternate ports, logs and initial setup.

## Keep wallet access

Connect a local test wallet, then choose **Save encrypted wallet backup**.
Keep the downloaded file and password and check that the download completed.
After reload, unlock the encrypted browser backup or restore the saved file.
No private key/password is sent to the faucet server. The unlocked key exists
in memory and is discarded on reload/disconnect. An encrypted backup cannot
restore a lost validator ledger or protect a compromised unlocked page.

Public transaction receipts are saved before broadcasting. If confirmation is
uncertain, use **Check transaction** before another trade. Do not clear storage
with unresolved transactions, and use one signing tab/tool per wallet. There
is no automatic transaction rebroadcast. Extension-wallet signing remains
implemented but unverified; local-wallet signing was exercised end to end.

## Choose the GoChain network deliberately

The original development chain and the original AMM testnet retain their own
ledgers and signing domains. They still have the reproduced historical-timestamp
weakness and experimental fixed-difficulty consensus. This release does not
silently migrate balances, replace their running binaries or change issuance.
Build updated legacy binaries explicitly to use the peer-mempool expiry fix.

For the new, explicitly trusted checkpoint network, follow `CHECKPOINTS.md`:

```powershell
go build -tags checkpoint -o gochain-checkpoint.exe .
```

It has a separate chain ID, ledger, port and supply, and requires an operator-pinned
authority public key and signed anchor. It preserves leap-day halvings, rejects
conflicting anchored histories/backdated extensions, and stops new work at stale
checkpoints or halving boundaries until renewal. Authority equivocation,
availability and correct clock/history approval are trusted. It is not a
permissionless consensus upgrade. Existing peer binaries remain separate.

## Verification

All state-changing verification must use disposable isolated ledgers/deployments.
Do not point the integration suite at a user's active pool.

```powershell
go test ./...
go test -tags amm ./...
go test -tags "amm checkpoint" ./...
go vet -tags "amm checkpoint" ./...
go test -race -tags "amm checkpoint" ./...
python -m unittest discover -s scripts -p test_reward_liquidity.py -v
```

From the isolated Solana checkout:

```powershell
npm ci
npm test
npm run build
npm run verify -- --isolated
npm run verify:faucet -- --isolated
```

The Rust native tests also run in the configured WSL toolchain with
`cargo test -p goamm --lib`; the SBF program builds using platform-tools v1.58.

Executed evidence includes legacy/AMM/checkpoint Go suites, checkpoint signature/
halving/fork tests, seven reward-helper tests, thirteen TypeScript/server tests,
four Rust tests, a production UI build, and isolated on-chain integration.
Browser checks cover encrypted file download, wrong password, reload/unlock,
file restoration, transaction cancellation, confirmed swap and receipt reload.
The faucet process test proves interrupted/completed claims prevent replay
despite lost summary entries and proves overlapping requests issue only once.

The on-chain suite checks both swap directions against integer quotes, deposits,
withdrawals, locked LP rejection, wrong-vault rejection, expiry, slippage, direct
donation behavior, and rollback after an actually executed second-transfer
failure. Token state rolls back on failure; the network fee still applies.
Runtime checkpoint verification mined, transferred 1 GO, renewed its authority
anchor, restarted and continued mining using an isolated GoChain ledger.

## Remaining boundaries

- Solana Demo GO is a separate test mint, not mined GoChain GO or backed wGO.
- testUSD is unbacked faucet money. Mint authority remains deliberately trusted.
- The faucet is loopback-only; Origin/Host checks are not authentication. New
  addresses can claim more test tokens. Never expose it as a public mint service.
- Claim files survive tested process interruption. No hardware power-loss test
  or unattended automatic reconciliation is claimed.
- Public devnet deployment remains unverified/unfunded; no mainnet deployment
  or real-value transaction was performed.
- Twelve inherited npm advisories remain (seven moderate, five high). Native
  bigint fallback, punycode deprecation and Anchor macro cfg warnings remain.
- Mining-reward automation coordinates helper runs on one machine, but other
  signing programs do not participate; separate journals authorize separate
  budgets. See AMM.md before using it.
- The trading simulator verifies deterministic accounting, not live-market
  profitability, arbitrage competitiveness, MEV resistance or execution latency.

Source and reviewer entry points are README.md, REVIEW_HANDOFF.md, AMM.md,
CHECKPOINTS.md and solana-devnet/README.md on `gochain-amm-review`.
