# Trusted-checkpoint GoChain test network

This opt-in build preserves leap-day halvings with an explicitly trusted
Ed25519 authority. It uses chain ID `gochain-checkpoint-amm-test-v1`, default
port 8084 and ledger `gochain-checkpoint-test.json`. It includes the GO/testUSD
AMM. It imports no existing supply. Existing ordinary and AMM builds, wallets,
and ledgers are not migrated or restarted.

## What the authority does

A signed certificate binds a chain ID, block height/hash and issue time. Operators
pin the authority public key independently of the certificate. Certificates do
not contain their own trusted key, and peer messages cannot replace the policy.

Every protected node requires the anchored block and rejects a conflicting fork,
even if it has more proof of work. Descendant timestamps must be at least the
checkpoint issue time. Normal reward replay still computes the leap-day reward
from each block timestamp.

Mining, transaction admission and peer adoption stop if the checkpoint is more
than 24 hours old, or if the local UTC clock crosses a reward-halving boundary
after its issue time. Historical audit remains possible after expiry. A fresh
certificate issued after the cutoff prevents descendants from claiming the old
reward. The receiver also rejects certificates more than 120 seconds ahead of
its own clock. Operators must keep their clocks accurate.

This is trusted finality and time anchoring, not permissionless security. The
authority can approve a bad history, equivocate, censor or stop renewing. Nodes
with different certificates/keys can disagree. Certificates have no automatic
distribution, authority rotation or quorum protocol. Operators must authenticate
updates, refuse rollback to older anchors, inspect the history before signing,
and coordinate renewal across peers. The signing CLI does not prevent an
authority from deliberately signing conflicting certificates.

Fixed difficulty, accelerated mining, low hash power and public-network defenses
remain experimental. This design does not enforce one block per minute or solve
all issuance/security problems. Do not treat these test assets as backed money.

## Isolated Windows setup

From the repository root, with Go installed:

```powershell
go build -tags checkpoint -o gochain-checkpoint.exe .
New-Item -ItemType Directory .checkpoint-local
.\gochain-checkpoint.exe wallet create --file .checkpoint-local\authority.wallet.json
.\gochain-checkpoint.exe wallet create --file .checkpoint-local\miner.wallet.json
$authority = (.\gochain-checkpoint.exe wallet address --file .checkpoint-local\authority.wallet.json | ConvertFrom-Json).public_key
.\gochain-checkpoint.exe checkpoint sign --genesis --wallet .checkpoint-local\authority.wallet.json --out .checkpoint-local\checkpoint-0.json
.\gochain-checkpoint.exe serve --port 8084 --data .checkpoint-local\chain.json --wallet .checkpoint-local\miner.wallet.json --checkpoint .checkpoint-local\checkpoint-0.json --authority $authority
```

The authority wallet is private and unencrypted on disk. Use a dedicated test
key, protect/back it up, and never put it in the browser or commit it. Only the
certificate and authority public key are distributed to validators. The folder
above is excluded from Git. File creation is exclusive: rerunning creation or
signing does not overwrite existing keys/certificates.

In another terminal, mine explicitly:

```powershell
.\gochain-checkpoint.exe mine --node http://127.0.0.1:8084 --wallet .checkpoint-local\miner.wallet.json
Invoke-RestMethod http://127.0.0.1:8084/stats
```

Automatic mining is optional: add `--mine --interval 1m` to serve. All AMM
commands need `--node http://127.0.0.1:8084`; use fresh test funding on this
network. The mining-reward helper intentionally remains restricted to the
original AMM network and must not be pointed at this one.

## Renewal

Stop the node cleanly, inspect its history, then sign its current tip with a
new output filename:

```powershell
.\gochain-checkpoint.exe checkpoint sign --data .checkpoint-local\chain.json --wallet .checkpoint-local\authority.wallet.json --out .checkpoint-local\checkpoint-next.json
.\gochain-checkpoint.exe validate --data .checkpoint-local\chain.json --checkpoint .checkpoint-local\checkpoint-next.json --authority $authority
```

Restart using the new certificate. Distribute the approved certificate and
matching anchored history to peers through an authenticated operator channel.
Renew before 24 hours and immediately after a halving cutoff. At the cutoff,
new work stops until renewal; this is intentional. A fresh node cannot start
from genesis using a non-genesis certificate: provide its approved ledger first.
Offline mining is disabled; use the node that enforces the loaded policy.

## Verified evidence

Tests cover signature/key/domain binding, modified certificates, halving cutoff
and stale-policy rejection, backdated descendants, conflicting forks, mutation
safety, and refusal to serve without policy before creating a ledger.

```powershell
go test -tags "amm checkpoint" ./...
go vet -tags "amm checkpoint" ./...
go test -race -tags "amm checkpoint" -run "TestCheckpoint|TestAMMPeerRejectsExpiredPendingAtNextHeight" .
```

An isolated runtime on port 18084 created a dedicated authority/miner/recipient,
signed genesis, mined two blocks, transferred 1 GO, stopped cleanly, renewed at
height 2, restarted and mined height 3. Supply was 30 GO, transferred counter
1 GO, checkpoint height 2, readiness true, and historical validation succeeded.
No existing user ledger or service was changed. This is not a production audit.
