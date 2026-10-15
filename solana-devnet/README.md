# Go Pools — independent Solana AMM prototype

This is our own Rust/Anchor AMM and browser interface. It borrows DEX usability
lessons (clear quotes, approvals, reserve visibility, liquidity workflows) from
projects such as Orca. It does not use Orca's program, SDK, branding, or routes.

The first version uses x*y=k, a fixed 0.3% input fee retained in pool reserves,
and fungible full-range LP shares. Concentrated liquidity, selectable price
ranges, routing, historical volume, APR, bridging, and production security are
not implemented. This is separate from GoChain and mined GO. These SPL GO/testUSD
mints have no backing or conversion relationship with GoChain assets.

## Current verified state

The custom program was compiled to SBF, deployed on a local Solana validator,
and initialized with two new SPL Token mints and 1,000 GO / 100,000 testUSD.
Swaps in both directions, deposits, withdrawals, and browser signing were tested.
The app visibly labels its network. Localnet is NOT Solana devnet.

Public devnet funding was attempted but the faucet returned an internal error,
then HTTP 429 (airdrop limit / dry faucet). This program and pool have NOT been
deployed to public devnet. The same setup tooling supports devnet once funded.

Dedicated test payer: `B7AnMS3BGUKSoB1FsqN9B1E53xxBn5epr1HGDcx49jYo`

Custom program ID: `4TdBWFzDUrn4Y5fB9mNJHWpdPP8PNyPnxY7s7FcN7AtK`

Only localnet currently has that program deployed. A new checkout must generate
its own keys/program ID; private keys are deliberately excluded from Git.

## Try the app

### Transaction status and recovery

Swaps and liquidity operations report preparing, wallet approval, pending and
confirmed states. The app saves the signed transaction signature and blockhash
expiry height before broadcasting. If browser storage is unavailable, it refuses
to submit. Public receipts contain no private key or password and survive reload
under the same wallet/network/origin. The activity list itself remains session-only.

If submission or confirmation is interrupted, the outcome is **unconfirmed**, not
automatically failed. The app does not automatically resend or re-sign, and blocks
another trade until **Check transaction** finds confirmation, confirmed failure,
or absence after blockhash expiry. It searches historical signature status and
rechecks status after observing expiry. Reconnect the same wallet after reload
to recover its receipt. Keep one signing tab/tool per wallet; this browser guard
is not an atomic global nonce service across tabs or independent wallet programs.
Do not clear browser storage while a transaction is unresolved. RPC correctness
and availability remain trusted; confirmed Solana state is not irreversible finality.

### Faucet durability

The local faucet remains an unauthenticated test mint service, restricted to a
loopback bind and exact Origin/Host. These headers are not authentication and
new wallet addresses can request more test tokens. It must not be exposed publicly.

Before funding, the server exclusively creates and syncs a per-wallet/network/
genesis claim in `.local/faucet-claims/`. Claim existence blocks duplicate funding
across processes and restarts, even if the summary journal is missing or a final
log line is torn. Confirmed transaction signatures are appended and synced as
funding progresses. Interrupted claims are never automatically retried. Inspect
the claim and on-chain balances manually before any operator reconciliation;
deleting claim files can re-enable issuance. Private keys remain server-only.

### Windows launcher (existing deployment)

From the repository root:

```powershell
.\scripts\Start-GoPools.ps1 -CheckOnly
.\scripts\Start-GoPools.ps1
```

The launcher reads the configured loopback RPC port, verifies local RPC health, rejects known public-cluster genesis
identities, checks the configured program/pool/token accounts, and reuses the UI
if it already serves this deployment. Otherwise it starts the installed Vite
server on loopback with logs under `.local/`. It never initializes, deploys,
resets, kills, or replaces a ledger. `-NoBrowser` skips opening the browser.
`-Port 5175` selects a different UI port; `-CheckOnly` checks both RPC/accounts
and that the UI serves the expected deployment, without launching services.

If the validator is stopped, explicitly identify its existing WSL ledger:

```powershell
.\scripts\Start-GoPools.ps1 -StartValidator -ValidatorLedger /home/YOUR_USER/path/to/existing-ledger
```

The path must contain the existing `genesis.bin`; do not substitute a fresh
directory. Ubuntu WSL must have `solana-test-validator` on its login-shell PATH.
First-time setup still follows the build/deploy instructions below. Both reuse
and startup from stopped validator/UI services were verified on an isolated
ledger (RPC 8997/UI 5175), preserving its genesis, deployed accounts and balances.
The launcher assigns gossip/dynamic ports from RPC+20 through RPC+50 and faucet
RPC+10; conflicting ports fail rather than stopping another process.

### Persistent local test wallets

`src/wallet-backup.ts` implements a versioned, localnet-only encrypted backup
format using Web Crypto PBKDF2-SHA256 (600,000 iterations) and AES-256-GCM.
The local genesis identity and wallet address are authenticated. Tests cover
restoration, randomization, wrong passwords, tampering, and different ledgers.
Choose a local session wallet, then **Save encrypted wallet backup**. Enter and
confirm a password of at least 12 characters. Keep the downloaded encrypted JSON
file and password. Optionally remember its ciphertext in this browser. After a
reload, choose **Connect wallet → Unlock saved wallet**, or choose **Restore
backup** and paste the encrypted file contents. Never paste a seed phrase.
Disconnect drops the unlocked key but retains any encrypted browser backup.
Saving another wallet does not overwrite a different wallet's browser backup;
use its downloaded file instead. Each origin/port has separate browser storage.

No plaintext keys or passwords are written to storage by this module. A backup does not
restore a lost validator ledger and cannot protect an already-compromised page.
Browser QA verified a download, wrong-password rejection, reload/unlock to the
same address and balances, restoration from the downloaded file, and a confirmed
swap signed by the restored wallet. All tests used isolated local test assets.

Open http://127.0.0.1:5174/ while the app server and local validator are running.

1. Connect wallet → Try a local session wallet.
2. Get test tokens (200 GO, 20,000 testUSD and local test SOL).
3. Get quote → Review swap → approve the test transaction.
4. My liquidity → preview and approve a deposit or withdrawal.

The unlocked local wallet signs inside the browser. Its in-memory key vanishes
on reload/disconnect; restore a saved encrypted backup to regain access. Without
a backup, it remains temporary. This wallet feature is localnet-only. For devnet, connect an
installed Phantom or Solflare wallet and fund it with DEVNET SOL. Never import a
seed phrase here. Extension connection/signing paths are implemented but require
an installed wallet and user approval; browser QA used the local session wallet.

Pool explorer shows program, pool, token-mint and LP-mint addresses. Activity is
session-local and records confirmed signatures; it is not a historical indexer.
The displayed exchange rate is a reserve ratio in unbacked testUSD.

## Fresh setup

Windows commands run from `C:\Projects\nothingchain\solana-devnet`. Rust/Solana
build/deployment use Ubuntu/WSL. Tested: Solana CLI 3.0.15, platform-tools v1.58,
Anchor crates 0.32.1, Node 22.12. Platform tools v1.51 failed parsing edition2024
dependencies, so the build script pins v1.58. Anchor emits macro cfg warnings.

```powershell
npm ci
npm run keys
```

The keys command creates `.local/deployer.json` and `.local/goamm-program.json`
only when absent. It synchronizes our program ID in Rust, the client interface,
Anchor.toml and TypeScript. Back these unencrypted signing keys up privately.
Keys, logs, deployment configuration, build output and node_modules are ignored
by Git. Vite explicitly denies HTTP access to private local and build folders.

In a WSL terminal, start a dedicated validator and keep it running:

```bash
solana-test-validator --ledger "$HOME/go-pools-ledger" --rpc-port 8897 --faucet-port 9907 --bind-address 127.0.0.1 --quiet
```

Do not reset a ledger containing a pool you intend to keep. The verification
instance uses `/home/wner/gochain-solana-test/orca-ledger` from an earlier tool
probe; the directory name is historical. Our program does not call Orca; a fresh
validator needs no cloned external AMM program or configuration accounts.

In another WSL terminal, fund the local test payer, build and deploy:

```bash
cd /mnt/c/Projects/nothingchain/solana-devnet
solana airdrop 10 --url http://127.0.0.1:8897 --keypair .local/deployer.json
bash scripts/deploy.sh localnet
```

Back in PowerShell:

```powershell
npm run setup:local
npm run dev
```

Setup creates test mints, associated accounts, a pool and initial liquidity.
Public configuration goes in `public/deployment.json`, with checkpoints in
`.local/`. Preserve checkpoints with their validator ledger; a reset validator
requires fresh localnet setup checkpoints. The faucet holds mint authority only
for these test mints. Review mint authorities before designing any real asset.

## Public devnet

Run `npm run keys`. Request DEVNET SOL for the printed payer at
https://faucet.solana.com. Several test SOL may be needed for program rent.
Do not send real SOL.

```bash
# WSL, project directory
bash scripts/deploy.sh devnet
```

```powershell
# After successful deployment
npm run setup
npm run dev
```

Setup writes devnet configuration only when it completes. Switching networks
requires reloading and reconnecting a suitable wallet. RPC/genesis checks reject
mainnet; local mode rejects known public clusters. Deployment accepts only
localnet/devnet.

The test-token faucet does not send treasury SOL on devnet. The connected wallet
must have faucet SOL first, then can request test tokens. The helper journals an
attempt before minting and refuses automatic retries following ambiguous/partial
failure. Inspect its local journal before manual recovery. Keep the Vite server
bound to loopback.

## Rules and verification

On-chain constraints check token mints, account authorities and vault addresses.
Pool addresses derive from sorted mints. Only the pool PDA can mint LP shares or
spend vaults. Initial sqrt(a*b) shares include 1,000 permanently locked LP atoms.
Later deposits round share issuance down and actual debits up. Withdrawals and
swap outputs round down. Arithmetic uses checked u64/u128; oversized products
reject. Client amounts use BigInt and six decimals. All writes carry minimum
outputs/shares and an expiry slot. UI limits allow 0.5% tolerance. Network fees
and account rent are separate. Token movements are atomic within each transaction.

Anyone can donate to a vault without receiving shares. Donations change reserves
and withdrawal claims, so the reserve ratio must not be used as a trusted oracle.

```powershell
npm test
npm run build
npm run verify -- --isolated
npm run verify:faucet -- --isolated
```

These commands must target a fresh disposable local deployment. `verify:faucet`
starts/stops its own server on port 5186, pauses it after a durable claim but
before funding, removes only its generated test address from the summary,
restarts, and proves the remaining claim prevents replay without issuing assets.
It also overlaps successful funding requests, verifies exactly one issuance,
then removes that test address's summary entry and proves the completed claim
still blocks replay after another restart.
It has no production fault-injection switch. Its reports stay in `.local/`.

`verify` runs only on localnet and changes the local pool. It tests both swap
directions, deposits/withdrawals, quote execution, fee/product behavior and
failure atomicity for impossible minimums, on-chain expiry, substituted vaults,
LP over-withdrawal and locked-LP redemption. It also checks that donations cannot
mint shares. Results go to a local verification log. GoChain/devnet are untouched.

```bash
CARGO_TARGET_DIR="$HOME/.cache/goamm-native-target" cargo test -p goamm --lib
```

Rust tests cover proportions, output/product, dust, overflow and sqrt bounds.
The checked-in IDL mirrors the small account/instruction layout;
`scripts/generate-idl.ts` reproduces it. Integration checks it against the deployed
binary. Rebuild/redeploy and verify after program changes. A Linux-native target
directory avoids slow cross-filesystem native compilation under WSL.

## Limits

This implementation has no independent audit. Program upgrades remain controlled
by the deployment authority. Mainnet, real assets and internet hosting are out of
scope. The server faucet and mint authority are local test conveniences.

`npm audit` reports inherited advisories in the current Anchor/Web3/SPL tree,
including bigint-buffer, toml, uuid and stream-json. Native bigint bindings are
unavailable here and the package uses its JS fallback; that does not establish
that the advisories are resolved. This is a local test prototype.

## Separate trusted local GO bridge

See [../BRIDGE.md](../BRIDGE.md) for the new local bridge UI on port 5180 and
its separate eight-decimal wGO/testUSD pool. This page's original six-decimal
Demo GO pool remains unrelated to mined GO. The bridge uses dedicated server-held
test wallets on a separate checkpoint GoChain ledger, not an extension wallet or
public network. `npm run bridge -- status`, `npm run bridge:ui` and
`npm run verify:bridge -- --isolated` are documented there.
