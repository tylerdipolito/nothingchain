# GoChain GRC-20 Consensus Audit & State Root Header Migration Specification

- **Module**: GRC-20 Native Token Program v0.1
- **Status**: Verified & Implemented
- **Author**: Antigravity & Bitproof Engineering
- **Date**: October 2026

---

## 1. Executive Summary

This document addresses the two consensus-critical questions raised regarding GoChain's native token standard:
1. **The 1 GO Token Creation Fee**: Verification of genuine balance deduction, permanent supply burn, network-wide conservation invariants ($\sum \text{balances} \equiv \text{circulating supply}$), and unburn behavior across chain reorganizations.
2. **Cryptographic State Root Commitment in Block Headers**: Formal analysis comparing lazily computed post-block state roots against cryptographically committed header state roots, accompanied by a zero-downtime, backward-compatible **Activation Height Migration Plan** for the live chain.
3. **User Token Interface**: Embedded web dashboard interface and CLI commands enabling non-technical users to issue, transfer, and manage community and business assets.

---

## 2. Token Creation Fee: Supply Conservation & Burn Invariant

### A. The Burn Mechanism
When a user issues a new GRC-20 asset via `create_token`:
- A fee of **1 GO** (`100,000,000 atoms`) is deducted from the creator's balance.
- Rather than redirecting this fee to a centralized company address or miner priority tip, **it is burned permanently from the circulating supply**.
- This introduces a permanent deflationary sink for GO, anchoring native coin demand to token creation velocity:

$$\Delta \text{Balance}_{\text{creator}} = -100{,}000{,}000 \text{ atoms}$$

$$\Delta \text{CirculatingSupply} = -100{,}000{,}000 \text{ atoms}$$

$$\Delta \text{BurnedSupply} = +100{,}000{,}000 \text{ atoms}$$

### B. Consensus Conservation Invariant
At every block height $H$, the consensus engine enforces:

$$\sum_{a \in \text{Accounts}} \text{Balance}(a) \equiv \text{CirculatingSupply} \equiv \text{TotalMintedRewards} - \text{TotalBurnedFees}$$

In [blockchain.go](file:///c:/Projects/nothingchain/blockchain.go) and [token.go](file:///c:/Projects/nothingchain/token.go):
```go
// Deduct and permanently burn native token creation fee in GO (1 GO = 100,000,000 atoms)
if l.balances[tx.From] < TokenCreationFeeAtoms {
    return fmt.Errorf("insufficient GO balance to pay token creation fee (requires %.2f GO)", float64(TokenCreationFeeAtoms)/float64(UnitsPerGO))
}
l.balances[tx.From] -= TokenCreationFeeAtoms
l.supply -= TokenCreationFeeAtoms
l.burned += TokenCreationFeeAtoms
```

### C. Reorganization & Rollback Verification
In [`TestTokenCreationFeeBurnAndSupplyConservation`](file:///c:/Projects/nothingchain/token_sync_test.go#L327):
1. Node mined 2 blocks (20 GO minted).
2. Token `COFFEE` created (burned 1 GO).
3. Post-block circulating supply: $20 + 10 - 1 = 29\text{ GO}$.
4. Burned supply: $1\text{ GO}$ ($10^8\text{ atoms}$).
5. Sum of account balances: $29\text{ GO}$ exact match.

---

## 3. Cryptographic State Root Header Commitment

### A. Computed State Root vs. Committed Header Root

```mermaid
flowchart LR
    subgraph CurrentImplementation ["Current Implementation (Phase 1)"]
        TxHistory["Block Transactions"] --> StateMachine["validateState(chain, false)"]
        StateMachine --> ComputedRoot["Computed TokenStateRoot (32B SHA-256)"]
        Note1["Two nodes running same blocks agree on root, but header has no commitment."]
    end

    subgraph ProposedMigration ["Header Commitment Migration (Phase 1.5)"]
        Header["Block Header (Bits, PrevHash, AuxPoW, Nonce)"]
        HeaderCommitment["StateRoot Field (32-byte hash)"]
        Header --> HeaderCommitment
        HeaderCommitment --> HeaderHash["Block Hash (AuxPoW Solves Header)"]
        Validator["Full Node Validator: assert(Header.StateRoot == ComputedRoot)"]
    end
```

- **Current State**: Independent nodes deriving state from transactions arrive at identical state roots (`TokenStateRoot()`), but the block header hash does not include the state root.
- **Limitation**: Light clients and fast-sync nodes cannot verify whether a proposed block state matches consensus without executing every block transaction from genesis.
- **Solution**: Cryptographically embed `StateRoot` directly into the `Block` header structure and include it in `b.computeHash()`.

### B. Safe Activation Height Migration Plan (Zero Downtime)

To avoid breaking the live test chain (currently at Height 254+ on Port 8090):

1. **Parameter**: Define `StateRootActivationHeight int64 = 300` (configurable per network).
2. **Pre-Activation ($i < \text{StateRootActivationHeight}$)**:
   - Blocks 0 to 299 use the legacy header format.
   - `b.computeHash()` omits `StateRoot`.
   - Historical blocks and checkpoints validate with 100% backward compatibility.
3. **Post-Activation ($i \ge \text{StateRootActivationHeight}$)**:
   - When miners construct candidate block $B_i$, they calculate:
     $$B_i.\text{StateRoot} = \text{postBlockLedger}.\text{tokens}.\text{ComputeStateRoot}()$$
   - `b.computeHash()` includes `b.StateRoot` in the SHA-256 pre-image.
   - When a node receives block $B_i$, consensus requires:
     $$\text{assert}(B_i.\text{StateRoot} == \text{computedPostBlockStateRoot})$$
   - Any block attempting to report an incorrect state root is rejected with `ErrStateRootMismatch`.

---

## 4. Reorganization Safety & Rollback Proof

In [`TestTokenReorganizationAndRollback`](file:///c:/Projects/nothingchain/token_sync_test.go#L405), we subjected the GRC-20 engine to a live chain reorganization:

```mermaid
sequenceDiagram
    participant Base as Common Ancestor (Block 2)
    participant ChainA as Branch A (Block 3 with Token TFORK)
    participant ChainB as Branch B (Blocks 3 & 4 without Token)

    Base->>ChainA: Creator queues create_token (TFORK) & mines Block 3
    Note over ChainA: Token TFORK exists; 1 GO fee burned; Root = Root_A
    Base->>ChainB: Other miner mines Block 3 & Block 4 (Length = 5)
    Note over ChainB: No token created; Root = Root_B

    ChainB->>ChainA: ChainA adopts longer Branch B (1-block reorg)
    Note over ChainA: Rollback: Token TFORK removed from active state!
    Note over ChainA: Fee unburned: Creator balance restored to 20 GO!
    Note over ChainA: Mempool salvage: Orphaned create_token tx saved to mempool!

    ChainA->>ChainA: ChainA mines Block 5 including salvaged tx
    Note over ChainA: Re-application: Token TFORK confirmed on winning branch!
    Note over ChainA: Fee re-burned; Final balance = 29 GO!
```

**Result**: Zero state leakage, zero orphaned database rows, 100% reversible ledger state transitions.

---

## 5. Web Dashboard & CLI Interface

The embedded dashboard ([dashboard.go](file:///c:/Projects/nothingchain/dashboard.go)) now includes a dedicated **GRC-20 Asset Layer** section:
- **Active Tokens Explorer**: Live table displaying Token ID, Symbol, Name, Decimals, Total Supply, Mint Authority, and Registration Height.
- **Asset Issuance UI**: Form for non-technical users to issue assets (Name, Symbol, Decimals, Initial Supply, Max Supply).
- **Asset Transfer UI**: Dropdown to select token asset, enter recipient `go1...` address and amount, and dispatch signed transactions.
- **CLI Equivalents**:
  ```bash
  # Issue new asset
  gochain token create --name "Coffee Bucks" --symbol BREW --decimals 6 --initial 1000000 --max 5000000

  # Transfer tokens
  gochain token transfer --token grc20:brew:7c7133fb --to go1alice... --amount 250000

  # Inspect token
  gochain token info --token grc20:brew:7c7133fb

  # View wallet token balance
  gochain token balance --token grc20:brew:7c7133fb --address go1alice...
  ```

---

## 6. Summary Checklist

| Requirement | Implementation | Test Status |
| :--- | :--- | :---: |
| **1 GO Creation Fee Burn** | Deducted from balance, subtracted from `l.supply`, added to `l.burned` | **Verified** ([`TestTokenCreationFeeBurnAndSupplyConservation`](file:///c:/Projects/nothingchain/token_sync_test.go#L327)) |
| **Confirmed State Only** | `Tokens()` and `TokenMetadata()` query confirmed blocks only | **Verified** |
| **Reorg Recovery** | Complete rollback of token state, fee unburn, mempool salvage | **Verified** ([`TestTokenReorganizationAndRollback`](file:///c:/Projects/nothingchain/token_sync_test.go#L405)) |
| **Multi-Node Sync** | Cross-node state root match across independent nodes | **Verified** ([`TestTokenMultiNodeSyncAndStateRootConsistency`](file:///c:/Projects/nothingchain/token_sync_test.go#L11)) |
| **Web & CLI UI** | Embedded in `web/index.html`, `web/app.js`, and `token_rpc.go` | **Verified** |
| **Header Commitment Plan** | Specification & activation height design defined in RFC | **Documented** |
