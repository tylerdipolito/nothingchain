package main

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTokenMultiNodeSyncAndStateRootConsistency(t *testing.T) {
	// 1. Initialize Node 1
	bc1 := NewBlockchain()
	creator := newTestWallet(t)
	alice := newTestWallet(t)
	bob := newTestWallet(t)

	// Mine 2 initial blocks to fund creator with 20 GO
	b1, err := bc1.MinePending(creator.Address)
	if err != nil {
		t.Fatalf("mine block 1: %v", err)
	}
	_ = b1
	b2, err := bc1.MinePending(creator.Address)
	if err != nil {
		t.Fatalf("mine block 2: %v", err)
	}
	_ = b2

	acctCreator, err := bc1.Account(creator.Address)
	if err != nil || acctCreator.Balance < 20*UnitsPerGO {
		t.Fatalf("creator funding failed: %+v", acctCreator)
	}

	// Step A: Instruction 1 - create_token "BREW" (Initial: 1,000,000, Max: 5,000,000, Decimals: 6)
	createOp := TokenInstruction{
		Op:            OpCreateToken,
		Name:          "Coffee Bucks",
		Symbol:        "BREW",
		Decimals:      6,
		InitialSupply: 1_000_000,
		MaxSupply:     5_000_000,
		MintAuthority: &creator.Address,
	}
	createMemo, _ := EncodeTokenInstruction(createOp)
	txCreate, err := creator.Sign(creator.Address, 0, createMemo, acctCreator.NextNonce)
	if err != nil {
		t.Fatalf("sign create: %v", err)
	}
	if err := bc1.AddTx(txCreate); err != nil {
		t.Fatalf("add create tx: %v", err)
	}
	if _, err := bc1.MinePending(creator.Address); err != nil {
		t.Fatalf("mine block 3: %v", err)
	}
	tokenID := GenerateTokenID(creator.Address, "BREW", txCreate.Nonce)

	// Step B: Instruction 2 - transfer_token: creator sends 250,000 BREW to alice
	acctCreator, _ = bc1.Account(creator.Address)
	xferOp := TokenInstruction{
		Op:        OpTransferToken,
		TokenID:   tokenID,
		Recipient: alice.Address,
		Amount:    250_000,
	}
	xferMemo, _ := EncodeTokenInstruction(xferOp)
	txXfer, err := creator.Sign(creator.Address, 0, xferMemo, acctCreator.NextNonce)
	if err != nil {
		t.Fatalf("sign xfer: %v", err)
	}
	if err := bc1.AddTx(txXfer); err != nil {
		t.Fatalf("add xfer tx: %v", err)
	}
	block4, err := bc1.MinePending(creator.Address)
	if err != nil {
		t.Fatalf("mine block 4: %v", err)
	}

	// Step C: Instruction 3 - mint: creator mints 500,000 BREW to bob
	acctCreator, _ = bc1.Account(creator.Address)
	mintOp := TokenInstruction{
		Op:        OpMint,
		TokenID:   tokenID,
		Recipient: bob.Address,
		Amount:    500_000,
	}
	mintMemo, _ := EncodeTokenInstruction(mintOp)
	txMint, err := creator.Sign(creator.Address, 0, mintMemo, acctCreator.NextNonce)
	if err != nil {
		t.Fatalf("sign mint: %v", err)
	}
	if err := bc1.AddTx(txMint); err != nil {
		t.Fatalf("add mint tx: %v", err)
	}
	block5, err := bc1.MinePending(creator.Address)
	if err != nil {
		t.Fatalf("mine block 5: %v", err)
	}

	// Step D: Instruction 4 - burn: alice burns 50,000 BREW
	acctAlice, _ := bc1.Account(alice.Address)
	burnOp := TokenInstruction{
		Op:      OpBurn,
		TokenID: tokenID,
		Amount:  50_000,
	}
	burnMemo, _ := EncodeTokenInstruction(burnOp)
	txBurn, err := alice.Sign(alice.Address, 0, burnMemo, acctAlice.NextNonce)
	if err != nil {
		t.Fatalf("sign burn: %v", err)
	}
	if err := bc1.AddTx(txBurn); err != nil {
		t.Fatalf("add burn tx: %v", err)
	}
	block6, err := bc1.MinePending(creator.Address)
	if err != nil {
		t.Fatalf("mine block 6: %v", err)
	}

	// Step E: Instruction 5 - set_authority: creator permanently revokes mint authority
	acctCreator, _ = bc1.Account(creator.Address)
	revokeOp := TokenInstruction{
		Op:            OpSetAuthority,
		TokenID:       tokenID,
		AuthorityType: "mint",
		NewAuthority:  nil,
	}
	revokeMemo, _ := EncodeTokenInstruction(revokeOp)
	txRevoke, err := creator.Sign(creator.Address, 0, revokeMemo, acctCreator.NextNonce)
	if err != nil {
		t.Fatalf("sign revoke: %v", err)
	}
	if err := bc1.AddTx(txRevoke); err != nil {
		t.Fatalf("add revoke tx: %v", err)
	}
	block7, err := bc1.MinePending(creator.Address)
	if err != nil {
		t.Fatalf("mine block 7: %v", err)
	}

	_ = block4
	_ = block5
	_ = block6
	_ = block7

	// Step F: Verify Attack Rejection - attempt minting after revocation
	acctCreator, _ = bc1.Account(creator.Address)
	attackMintOp := TokenInstruction{
		Op:        OpMint,
		TokenID:   tokenID,
		Recipient: creator.Address,
		Amount:    100_000,
	}
	attackMemo, _ := EncodeTokenInstruction(attackMintOp)
	txAttack, _ := creator.Sign(creator.Address, 0, attackMemo, acctCreator.NextNonce)
	if err := bc1.AddTx(txAttack); err == nil {
		t.Fatal("expected attack mint after revocation to be rejected from mempool")
	}

	// Check Node 1 State
	root1, err := bc1.TokenStateRoot()
	if err != nil {
		t.Fatalf("node 1 state root: %v", err)
	}
	meta1, err := bc1.TokenMetadata(tokenID)
	if err != nil {
		t.Fatalf("node 1 metadata: %v", err)
	}
	balCreator1, _ := bc1.TokenBalance(tokenID, creator.Address)
	balAlice1, _ := bc1.TokenBalance(tokenID, alice.Address)
	balBob1, _ := bc1.TokenBalance(tokenID, bob.Address)

	// Math Check:
	// Initial = 1,000,000
	// Creator sent 250,000 to Alice -> Creator = 750,000
	// Creator minted 500,000 to Bob -> Bob = 500,000
	// Alice burned 50,000 -> Alice = 200,000
	// Total supply = 750k + 500k + 200k = 1,450,000 base units (1.45 BREW)
	if meta1.TotalSupply != 1_450_000 {
		t.Fatalf("expected total supply 1,450,000, got %d", meta1.TotalSupply)
	}
	if balCreator1 != 750_000 || balAlice1 != 200_000 || balBob1 != 500_000 {
		t.Fatalf("balances mismatch: creator=%d, alice=%d, bob=%d", balCreator1, balAlice1, balBob1)
	}

	// =========================================================================
	// 2. Multi-Node State Synchronization: Spin up independent Node 2
	// =========================================================================
	bc2 := NewBlockchain()
	adopted, err := bc2.Adopt(bc1.Snapshot())
	if err != nil || !adopted {
		t.Fatalf("node 2 failed to adopt chain from node 1: %v (adopted=%t)", err, adopted)
	}

	// Verify Node 2 derived state
	root2, err := bc2.TokenStateRoot()
	if err != nil {
		t.Fatalf("node 2 state root: %v", err)
	}
	meta2, err := bc2.TokenMetadata(tokenID)
	if err != nil {
		t.Fatalf("node 2 metadata: %v", err)
	}
	balCreator2, _ := bc2.TokenBalance(tokenID, creator.Address)
	balAlice2, _ := bc2.TokenBalance(tokenID, alice.Address)
	balBob2, _ := bc2.TokenBalance(tokenID, bob.Address)

	// STRICT CONSENSUS EQUIVALENCE ASSERTIONS
	if root1 != root2 {
		t.Fatalf("STATE ROOT DIVERGENCE BETWEEN NODES!\nNode 1 Root: %s\nNode 2 Root: %s", root1, root2)
	}
	if meta1.TotalSupply != meta2.TotalSupply {
		t.Fatalf("total supply mismatch: node1=%d, node2=%d", meta1.TotalSupply, meta2.TotalSupply)
	}
	if balCreator1 != balCreator2 || balAlice1 != balAlice2 || balBob1 != balBob2 {
		t.Fatalf("token balance divergence between nodes!")
	}
	if meta2.MintAuthority != "" {
		t.Fatalf("node 2 failed to enforce permanent mint authority revocation")
	}

	t.Logf("==================================================================================")
	t.Logf("🎉 MULTI-NODE GRC-20 CONSENSUS SYNCHRONIZATION VERIFIED!")
	t.Logf("   - Asset ID:        %s", tokenID)
	t.Logf("   - Token Symbol:    %s (%s)", meta2.Symbol, meta2.Name)
	t.Logf("   - Decimals:        %d", meta2.Decimals)
	t.Logf("   - Total Supply:    %s %s (%d base units)", FormatTokenAmount(meta2.TotalSupply, meta2.Decimals), meta2.Symbol, meta2.TotalSupply)
	t.Logf("   - Mint Authority:  Permanently Revoked (empty string: %q)", meta2.MintAuthority)
	t.Logf("   - Node 1 State:    Root: %s (Confirmed Height %d)", root1, bc1.Stats().Height)
	t.Logf("   - Node 2 State:    Root: %s (Confirmed Height %d)", root2, bc2.Stats().Height)
	t.Logf("   - Consensus Sync:  100%% DETERMINISTIC STATE ROOT MATCH: %t", root1 == root2)
	t.Logf("   - Balances:        Creator: %s | Alice: %s | Bob: %s",
		FormatTokenAmount(balCreator2, meta2.Decimals),
		FormatTokenAmount(balAlice2, meta2.Decimals),
		FormatTokenAmount(balBob2, meta2.Decimals))
	t.Logf("==================================================================================")
}

func TestTokenHTTPEndpointsEndToEnd(t *testing.T) {
	bc := NewBlockchain()
	creator := newTestWallet(t)
	_, _ = bc.MinePending(creator.Address)
	_, _ = bc.MinePending(creator.Address)

	handler := newHandlerMonitored(bc, creator.Address, false, time.Minute, nil)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	// 1. Test POST /token/create
	mAuth := creator.Address
	createReq := TokenRPCPayload{
		PrivateKey:    creator.Seed,
		Name:          "Test Bucks",
		Symbol:        "TBUX",
		Decimals:      4,
		InitialSupply: 10000,
		MaxSupply:     50000,
		MintAuthority: &mAuth,
	}
	var createRes struct {
		Status  string      `json:"status"`
		TokenID string      `json:"token_id"`
		Tx      Transaction `json:"tx"`
	}
	if err := request(ts.URL, "/token/create", createReq, &createRes); err != nil {
		t.Fatalf("http create failed: %v", err)
	}
	if createRes.Status != "queued" || createRes.TokenID == "" {
		t.Fatalf("unexpected create response: %+v", createRes)
	}

	// Mine a block to confirm
	_, err := bc.MinePending(creator.Address)
	if err != nil {
		t.Fatalf("mine block: %v", err)
	}

	// 2. Test GET /token/list
	var listRes struct {
		Count  int              `json:"count"`
		Tokens []*TokenMetadata `json:"tokens"`
	}
	if err := request(ts.URL, "/token/list", nil, &listRes); err != nil {
		t.Fatalf("http list failed: %v", err)
	}
	if listRes.Count != 1 || listRes.Tokens[0].Symbol != "TBUX" {
		t.Fatalf("unexpected list response: %+v", listRes)
	}

	// 3. Test GET /token/metadata/
	var metaRes TokenMetadata
	if err := request(ts.URL, "/token/metadata/"+createRes.TokenID, nil, &metaRes); err != nil {
		t.Fatalf("http metadata failed: %v", err)
	}
	if metaRes.Name != "Test Bucks" || metaRes.TotalSupply != 10000 {
		t.Fatalf("unexpected meta response: %+v", metaRes)
	}

	// 4. Test GET /token/balance/
	var balRes struct {
		TokenID   string `json:"token_id"`
		Address   string `json:"address"`
		Balance   int64  `json:"balance"`
		Decimals  uint8  `json:"decimals"`
		Formatted string `json:"formatted"`
	}
	if err := request(ts.URL, fmt.Sprintf("/token/balance/%s/%s", createRes.TokenID, creator.Address), nil, &balRes); err != nil {
		t.Fatalf("http balance failed: %v", err)
	}
	if balRes.Balance != 10000 || balRes.Formatted != "1.0000" {
		t.Fatalf("unexpected balance response: %+v", balRes)
	}

	// 5. Test GET /token/state-root
	var rootRes struct {
		StateRoot string `json:"state_root"`
		Height    int    `json:"height"`
	}
	if err := request(ts.URL, "/token/state-root", nil, &rootRes); err != nil {
		t.Fatalf("http state root failed: %v", err)
	}
	if len(rootRes.StateRoot) != 64 {
		t.Fatalf("invalid state root hex: %s", rootRes.StateRoot)
	}
	t.Logf("✅ Verified HTTP token endpoints: Root=%s, Height=%d, Supply=%s TBUX", rootRes.StateRoot, rootRes.Height, balRes.Formatted)
}

func TestTokenCreationFeeBurnAndSupplyConservation(t *testing.T) {
	bc := NewBlockchain()
	creator := newTestWallet(t)

	// Mine 2 blocks to creator -> 20 GO (2,000,000,000 atoms)
	if _, err := bc.MinePending(creator.Address); err != nil {
		t.Fatalf("mine block 1: %v", err)
	}
	if _, err := bc.MinePending(creator.Address); err != nil {
		t.Fatalf("mine block 2: %v", err)
	}

	statsBefore := bc.Stats()
	if statsBefore.TotalSupply != 20*UnitsPerGO {
		t.Fatalf("expected 20 GO initial supply, got %d", statsBefore.TotalSupply)
	}
	if statsBefore.BurnedSupply != 0 {
		t.Fatalf("expected 0 initial burned supply, got %d", statsBefore.BurnedSupply)
	}

	// Create GRC-20 token "COFFEE"
	acctCreator, _ := bc.Account(creator.Address)
	createOp := TokenInstruction{
		Op:            OpCreateToken,
		Name:          "Coffee Coin",
		Symbol:        "COFFEE",
		Decimals:      6,
		InitialSupply: 500_000,
		MaxSupply:     1_000_000,
		MintAuthority: &creator.Address,
	}
	memo, _ := EncodeTokenInstruction(createOp)
	tx, err := creator.Sign(creator.Address, 0, memo, acctCreator.NextNonce)
	if err != nil {
		t.Fatalf("sign tx: %v", err)
	}
	if err := bc.AddTx(tx); err != nil {
		t.Fatalf("add tx: %v", err)
	}

	// Mine block 3 to confirm token creation
	b3, err := bc.MinePending(creator.Address)
	if err != nil {
		t.Fatalf("mine block 3: %v", err)
	}
	_ = b3

	// Assertions on post-creation ledger state
	statsAfter := bc.Stats()
	// Block 3 minted 10 GO reward, but token creation burned exactly 1 GO fee
	// Expected net circulating supply = 20 GO + 10 GO - 1 GO = 29 GO (2,900,000,000 atoms)
	expectedSupply := int64(29 * UnitsPerGO)
	if statsAfter.TotalSupply != expectedSupply {
		t.Fatalf("supply invariant violation: expected %d atoms, got %d", expectedSupply, statsAfter.TotalSupply)
	}
	if statsAfter.BurnedSupply != TokenCreationFeeAtoms {
		t.Fatalf("expected burned supply %d atoms (1 GO), got %d", TokenCreationFeeAtoms, statsAfter.BurnedSupply)
	}
	if statsAfter.BurnedGO != "1" {
		t.Fatalf("expected BurnedGO '1', got %s", statsAfter.BurnedGO)
	}

	// Creator Balance Check:
	// Minted 3 blocks = 30 GO. Minus 1 GO creation fee = 29 GO
	acctCreatorAfter, _ := bc.Account(creator.Address)
	if acctCreatorAfter.Balance != expectedSupply {
		t.Fatalf("creator balance mismatch: expected %d, got %d", expectedSupply, acctCreatorAfter.Balance)
	}

	tokenID := GenerateTokenID(creator.Address, "COFFEE", tx.Nonce)
	tokenBal, err := bc.TokenBalance(tokenID, creator.Address)
	if err != nil || tokenBal != 500_000 {
		t.Fatalf("expected token balance 500,000, got %d (err: %v)", tokenBal, err)
	}

	t.Logf("✅ Verified 1 GO token creation fee burned from circulating supply: Net Supply=%s GO, Burned=%s GO", statsAfter.SupplyGO, statsAfter.BurnedGO)
}

func TestTokenReorganizationAndRollback(t *testing.T) {
	creator := newTestWallet(t)
	otherMiner := newTestWallet(t)

	// Setup common base chain (Genesis + 2 blocks mined to creator)
	chainA := NewBlockchain()
	if _, err := chainA.MinePending(creator.Address); err != nil {
		t.Fatalf("mine base 1: %v", err)
	}
	if _, err := chainA.MinePending(creator.Address); err != nil {
		t.Fatalf("mine base 2: %v", err)
	}

	// Fork base snapshot into chainB
	chainB := NewBlockchain()
	if _, err := chainB.Adopt(chainA.Snapshot()); err != nil {
		t.Fatalf("chainB failed to adopt base: %v", err)
	}

	// On Chain A: Creator queues token creation for "TFORK" and mines Block 3
	acct, _ := chainA.Account(creator.Address)
	createOp := TokenInstruction{
		Op:            OpCreateToken,
		Name:          "Fork Test Token",
		Symbol:        "TFORK",
		Decimals:      4,
		InitialSupply: 100_000,
		MaxSupply:     200_000,
		MintAuthority: &creator.Address,
	}
	memo, _ := EncodeTokenInstruction(createOp)
	tx, err := creator.Sign(creator.Address, 0, memo, acct.NextNonce)
	if err != nil {
		t.Fatalf("sign tx: %v", err)
	}
	if err := chainA.AddTx(tx); err != nil {
		t.Fatalf("add tx chainA: %v", err)
	}
	if _, err := chainA.MinePending(creator.Address); err != nil {
		t.Fatalf("mine block 3 chainA: %v", err)
	}

	tokenID := GenerateTokenID(creator.Address, "TFORK", tx.Nonce)
	metaA, err := chainA.TokenMetadata(tokenID)
	if err != nil || metaA.TotalSupply != 100_000 {
		t.Fatalf("expected token on chainA before reorg: %v", err)
	}
	rootA, _ := chainA.TokenStateRoot()

	// On Chain B: Other miner mines Block 3 and Block 4 (longer branch, 5 blocks vs 4)
	if _, err := chainB.MinePending(otherMiner.Address); err != nil {
		t.Fatalf("mine block 3 chainB: %v", err)
	}
	if _, err := chainB.MinePending(otherMiner.Address); err != nil {
		t.Fatalf("mine block 4 chainB: %v", err)
	}
	rootB, _ := chainB.TokenStateRoot()

	// Verify pre-conditions: roots differ because chainA has token, chainB does not
	if rootA == rootB {
		t.Fatal("expected state roots to differ between branches")
	}

	// STEP 1: Reorganization - Chain A adopts longer Chain B
	adopted, err := chainA.Adopt(chainB.Snapshot())
	if err != nil || !adopted {
		t.Fatalf("chainA failed to reorganize to chainB: %v (adopted=%t)", err, adopted)
	}

	// VERIFY ROLLBACK OF TOKEN STATE ON CHAIN A:
	// 1. Token must no longer exist on Chain A
	if _, err := chainA.TokenMetadata(tokenID); err == nil {
		t.Fatal("expected token to be rolled back and disappear from active state")
	}
	// 2. State root must match Chain B exactly
	reorgRootA, _ := chainA.TokenStateRoot()
	if reorgRootA != rootB {
		t.Fatalf("reorganized state root mismatch: expected %s, got %s", rootB, reorgRootA)
	}
	// 3. The 1 GO burned fee was unburned (creator balance on chainA matches base state of 20 GO)
	acctAAfterReorg, _ := chainA.Account(creator.Address)
	if acctAAfterReorg.Balance != 20*UnitsPerGO {
		t.Fatalf("expected creator balance restored to 20 GO, got %d", acctAAfterReorg.Balance)
	}
	// 4. Orphaned create_token transaction was saved back to chainA mempool
	if chainA.Stats().PendingTxs != 1 {
		t.Fatalf("expected 1 orphaned transaction returned to mempool, got %d", chainA.Stats().PendingTxs)
	}

	// STEP 2: Re-mining the transaction into the new chain
	if _, err := chainA.MinePending(creator.Address); err != nil {
		t.Fatalf("failed to mine block with re-applied token tx: %v", err)
	}

	// VERIFY RE-APPLIED TOKEN STATE ON CHAIN A:
	metaReapplied, err := chainA.TokenMetadata(tokenID)
	if err != nil || metaReapplied.TotalSupply != 100_000 {
		t.Fatalf("expected token re-applied cleanly after re-mining: %v", err)
	}
	// Creator now has: 20 GO (base) + 10 GO (mined block) - 1 GO (creation fee) = 29 GO
	acctFinal, _ := chainA.Account(creator.Address)
	if acctFinal.Balance != 29*UnitsPerGO {
		t.Fatalf("expected final balance 29 GO, got %d", acctFinal.Balance)
	}

	t.Logf("✅ 100%% REORGANIZATION SAFETY VERIFIED: Token rollback, fee unburn, mempool salvage, and re-application confirmed!")
}
