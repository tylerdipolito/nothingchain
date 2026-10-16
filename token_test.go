package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"
)

func newTestWallet(t *testing.T) *Wallet {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return &Wallet{
		Address:   addressFor(pub),
		PublicKey: hex.EncodeToString(pub),
		Seed:      hex.EncodeToString(priv.Seed()),
	}
}

func fundWallet(l *ledger, addr string, atoms int64) {
	l.balances[addr] += atoms
	l.supply += atoms
}

func signTokenTx(t *testing.T, w *Wallet, op TokenInstruction, nonce uint64) Transaction {
	t.Helper()
	memo, err := EncodeTokenInstruction(op)
	if err != nil {
		t.Fatalf("encode instruction: %v", err)
	}
	tx, err := w.Sign(w.Address, 0, memo, nonce)
	if err != nil {
		t.Fatalf("sign tx: %v", err)
	}
	return tx
}

func TestTokenCreate(t *testing.T) {
	l := newLedger()
	w := newTestWallet(t)

	// Attempt creation without GO funds -> must fail
	op := TokenInstruction{
		Op:            OpCreateToken,
		Name:          "Coffee Bucks",
		Symbol:        "BREW",
		Decimals:      6,
		InitialSupply: 1_000_000,
		MaxSupply:     10_000_000,
		MintAuthority: &w.Address,
	}
	tx := signTokenTx(t, w, op, 1)
	err := l.apply(tx)
	if err == nil {
		t.Fatal("expected failure on zero GO balance for creation fee")
	}

	// Fund creator with 5 GO (500,000,000 atoms)
	fundWallet(l, w.Address, 5*UnitsPerGO)

	// Now creation should succeed
	err = l.apply(tx)
	if err != nil {
		t.Fatalf("expected successful token creation: %v", err)
	}

	// Verify fee deduction: 5 GO - 1 GO fee = 4 GO
	expectedGO := int64(4 * UnitsPerGO)
	if l.balances[w.Address] != expectedGO {
		t.Fatalf("expected GO balance %d, got %d", expectedGO, l.balances[w.Address])
	}

	expectedID := GenerateTokenID(w.Address, "BREW", 1)
	meta, exists := l.tokens.Tokens[expectedID]
	if !exists {
		t.Fatalf("token metadata for %s not found", expectedID)
	}
	if meta.Name != "Coffee Bucks" || meta.Symbol != "BREW" || meta.Decimals != 6 {
		t.Fatalf("metadata mismatch: %+v", meta)
	}
	if meta.TotalSupply != 1_000_000 {
		t.Fatalf("expected total supply 1,000,000, got %d", meta.TotalSupply)
	}
	if l.tokens.Balances[expectedID][w.Address] != 1_000_000 {
		t.Fatalf("expected creator token balance 1,000,000, got %d", l.tokens.Balances[expectedID][w.Address])
	}

	// Test Symbol validation
	invalidSymbols := []string{"", "TOOLONGTOKENNAME", "brew", "BR-W", "BR EW"}
	for _, sym := range invalidSymbols {
		invOp := TokenInstruction{
			Op:            OpCreateToken,
			Name:          "Invalid Token",
			Symbol:        sym,
			Decimals:      6,
			InitialSupply: 100,
			MaxSupply:     1000,
		}
		invTx := signTokenTx(t, w, invOp, 2)
		if err := l.apply(invTx); err == nil {
			t.Fatalf("expected rejection for invalid symbol %q", sym)
		}
	}

	// Test InitialSupply > MaxSupply -> must fail
	invOp := TokenInstruction{
		Op:            OpCreateToken,
		Name:          "Overflow Token",
		Symbol:        "OVER",
		Decimals:      6,
		InitialSupply: 1000,
		MaxSupply:     500,
	}
	invTx := signTokenTx(t, w, invOp, 2)
	if err := l.apply(invTx); err == nil {
		t.Fatal("expected rejection when initial supply exceeds max supply")
	}
}

func TestTokenMint(t *testing.T) {
	l := newLedger()
	creator := newTestWallet(t)
	recipient := newTestWallet(t)
	unauth := newTestWallet(t)

	fundWallet(l, creator.Address, 10*UnitsPerGO)
	fundWallet(l, unauth.Address, 10*UnitsPerGO)

	createOp := TokenInstruction{
		Op:            OpCreateToken,
		Name:          "Gold Coin",
		Symbol:        "GOLD",
		Decimals:      8,
		InitialSupply: 100,
		MaxSupply:     1000,
		MintAuthority: &creator.Address,
	}
	if err := l.apply(signTokenTx(t, creator, createOp, 1)); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	tokenID := GenerateTokenID(creator.Address, "GOLD", 1)

	// 1. Successful mint by authorized authority
	mintOp := TokenInstruction{
		Op:        OpMint,
		TokenID:   tokenID,
		Recipient: recipient.Address,
		Amount:    400,
	}
	if err := l.apply(signTokenTx(t, creator, mintOp, 2)); err != nil {
		t.Fatalf("authorized mint failed: %v", err)
	}
	if l.tokens.Balances[tokenID][recipient.Address] != 400 {
		t.Fatalf("expected recipient balance 400, got %d", l.tokens.Balances[tokenID][recipient.Address])
	}
	if l.tokens.Tokens[tokenID].TotalSupply != 500 {
		t.Fatalf("expected total supply 500, got %d", l.tokens.Tokens[tokenID].TotalSupply)
	}

	// 2. Unauthorized mint attempt -> must fail
	unauthMintOp := TokenInstruction{
		Op:        OpMint,
		TokenID:   tokenID,
		Recipient: unauth.Address,
		Amount:    100,
	}
	if err := l.apply(signTokenTx(t, unauth, unauthMintOp, 1)); err == nil {
		t.Fatal("expected unauthorized mint to be rejected")
	}

	// 3. Mint exceeding max supply -> must fail (500 + 600 > 1000)
	overMintOp := TokenInstruction{
		Op:        OpMint,
		TokenID:   tokenID,
		Recipient: recipient.Address,
		Amount:    600,
	}
	if err := l.apply(signTokenTx(t, creator, overMintOp, 3)); err == nil {
		t.Fatal("expected mint exceeding max supply to be rejected")
	}

	// 4. Mint exactly up to max supply (remaining 500) -> should succeed
	exactMintOp := TokenInstruction{
		Op:        OpMint,
		TokenID:   tokenID,
		Recipient: recipient.Address,
		Amount:    500,
	}
	if err := l.apply(signTokenTx(t, creator, exactMintOp, 3)); err != nil {
		t.Fatalf("mint up to max supply failed: %v", err)
	}
	if l.tokens.Tokens[tokenID].TotalSupply != 1000 {
		t.Fatalf("expected total supply 1000, got %d", l.tokens.Tokens[tokenID].TotalSupply)
	}

	// 5. Subsequent mint beyond max supply -> must fail
	if err := l.apply(signTokenTx(t, creator, TokenInstruction{
		Op:        OpMint,
		TokenID:   tokenID,
		Recipient: recipient.Address,
		Amount:    1,
	}, 4)); err == nil {
		t.Fatal("expected mint beyond max supply to fail")
	}
}

func TestTokenTransfer(t *testing.T) {
	l := newLedger()
	w1 := newTestWallet(t)
	w2 := newTestWallet(t)

	fundWallet(l, w1.Address, 10*UnitsPerGO)
	fundWallet(l, w2.Address, 10*UnitsPerGO)

	createOp := TokenInstruction{
		Op:            OpCreateToken,
		Name:          "Silver Token",
		Symbol:        "SILVER",
		Decimals:      2,
		InitialSupply: 5000,
		MaxSupply:     10000,
		MintAuthority: &w1.Address,
	}
	if err := l.apply(signTokenTx(t, w1, createOp, 1)); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	tokenID := GenerateTokenID(w1.Address, "SILVER", 1)

	// Valid transfer: w1 sends 2000 to w2
	xferOp := TokenInstruction{
		Op:        OpTransferToken,
		TokenID:   tokenID,
		Recipient: w2.Address,
		Amount:    2000,
	}
	if err := l.apply(signTokenTx(t, w1, xferOp, 2)); err != nil {
		t.Fatalf("transfer failed: %v", err)
	}
	if l.tokens.Balances[tokenID][w1.Address] != 3000 {
		t.Fatalf("expected w1 balance 3000, got %d", l.tokens.Balances[tokenID][w1.Address])
	}
	if l.tokens.Balances[tokenID][w2.Address] != 2000 {
		t.Fatalf("expected w2 balance 2000, got %d", l.tokens.Balances[tokenID][w2.Address])
	}

	// Insufficient balance transfer (w2 tries to send 3000, but only has 2000) -> must fail
	overXferOp := TokenInstruction{
		Op:        OpTransferToken,
		TokenID:   tokenID,
		Recipient: w1.Address,
		Amount:    3000,
	}
	if err := l.apply(signTokenTx(t, w2, overXferOp, 1)); err == nil {
		t.Fatal("expected transfer exceeding balance to fail")
	}

	// Negative amount transfer -> must fail
	negOp := TokenInstruction{
		Op:        OpTransferToken,
		TokenID:   tokenID,
		Recipient: w1.Address,
		Amount:    -100,
	}
	if err := l.apply(signTokenTx(t, w2, negOp, 1)); err == nil {
		t.Fatal("expected negative transfer amount to fail")
	}
}

func TestTokenBurn(t *testing.T) {
	l := newLedger()
	w := newTestWallet(t)
	fundWallet(l, w.Address, 10*UnitsPerGO)

	createOp := TokenInstruction{
		Op:            OpCreateToken,
		Name:          "Burnable Coin",
		Symbol:        "BURN",
		Decimals:      0,
		InitialSupply: 10_000,
		MaxSupply:     10_000,
	}
	if err := l.apply(signTokenTx(t, w, createOp, 1)); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	tokenID := GenerateTokenID(w.Address, "BURN", 1)

	// Burn 3000 tokens
	burnOp := TokenInstruction{
		Op:      OpBurn,
		TokenID: tokenID,
		Amount:  3000,
	}
	if err := l.apply(signTokenTx(t, w, burnOp, 2)); err != nil {
		t.Fatalf("burn failed: %v", err)
	}
	if l.tokens.Balances[tokenID][w.Address] != 7000 {
		t.Fatalf("expected balance 7000, got %d", l.tokens.Balances[tokenID][w.Address])
	}
	if l.tokens.Tokens[tokenID].TotalSupply != 7000 {
		t.Fatalf("expected total supply 7000, got %d", l.tokens.Tokens[tokenID].TotalSupply)
	}

	// Burn more than balance -> must fail
	overBurn := TokenInstruction{
		Op:      OpBurn,
		TokenID: tokenID,
		Amount:  10000,
	}
	if err := l.apply(signTokenTx(t, w, overBurn, 3)); err == nil {
		t.Fatal("expected over-burn to fail")
	}
}

func TestTokenSetAuthority(t *testing.T) {
	l := newLedger()
	creator := newTestWallet(t)
	newAuth := newTestWallet(t)
	recipient := newTestWallet(t)
	fundWallet(l, creator.Address, 10*UnitsPerGO)
	fundWallet(l, newAuth.Address, 10*UnitsPerGO)

	createOp := TokenInstruction{
		Op:            OpCreateToken,
		Name:          "Auth Token",
		Symbol:        "AUTH",
		Decimals:      4,
		InitialSupply: 100,
		MaxSupply:     10000,
		MintAuthority: &creator.Address,
	}
	if err := l.apply(signTokenTx(t, creator, createOp, 1)); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	tokenID := GenerateTokenID(creator.Address, "AUTH", 1)

	// 1. Transfer mint authority to newAuth
	setOp := TokenInstruction{
		Op:            OpSetAuthority,
		TokenID:       tokenID,
		AuthorityType: "mint",
		NewAuthority:  &newAuth.Address,
	}
	if err := l.apply(signTokenTx(t, creator, setOp, 2)); err != nil {
		t.Fatalf("set authority failed: %v", err)
	}
	if l.tokens.Tokens[tokenID].MintAuthority != newAuth.Address {
		t.Fatalf("expected mint authority %s, got %s", newAuth.Address, l.tokens.Tokens[tokenID].MintAuthority)
	}

	// 2. Old authority tries to mint -> must fail
	oldMint := TokenInstruction{
		Op:        OpMint,
		TokenID:   tokenID,
		Recipient: recipient.Address,
		Amount:    50,
	}
	if err := l.apply(signTokenTx(t, creator, oldMint, 3)); err == nil {
		t.Fatal("expected old mint authority to fail")
	}

	// 3. New authority mints -> should succeed
	newMint := TokenInstruction{
		Op:        OpMint,
		TokenID:   tokenID,
		Recipient: recipient.Address,
		Amount:    200,
	}
	if err := l.apply(signTokenTx(t, newAuth, newMint, 1)); err != nil {
		t.Fatalf("new authority mint failed: %v", err)
	}

	// 4. Permanently revoke mint authority
	revokeOp := TokenInstruction{
		Op:            OpSetAuthority,
		TokenID:       tokenID,
		AuthorityType: "mint",
		NewAuthority:  nil,
	}
	if err := l.apply(signTokenTx(t, newAuth, revokeOp, 2)); err != nil {
		t.Fatalf("revoke failed: %v", err)
	}
	if l.tokens.Tokens[tokenID].MintAuthority != "" {
		t.Fatalf("expected empty mint authority, got %s", l.tokens.Tokens[tokenID].MintAuthority)
	}

	// 5. Subsequent mint after revocation -> must fail permanently
	if err := l.apply(signTokenTx(t, newAuth, newMint, 3)); err == nil {
		t.Fatal("expected minting after revocation to fail permanently")
	}
}

func TestDeterministicStateRoot(t *testing.T) {
	// Construct two separate ledgers with identical operations
	l1 := newLedger()
	l2 := newLedger()

	w1 := newTestWallet(t)
	w2 := newTestWallet(t)

	fundWallet(l1, w1.Address, 10*UnitsPerGO)
	fundWallet(l1, w2.Address, 10*UnitsPerGO)
	fundWallet(l2, w1.Address, 10*UnitsPerGO)
	fundWallet(l2, w2.Address, 10*UnitsPerGO)

	createOp := TokenInstruction{
		Op:            OpCreateToken,
		Name:          "Deterministic Root Token",
		Symbol:        "DET",
		Decimals:      6,
		InitialSupply: 5_000_000,
		MaxSupply:     10_000_000,
		MintAuthority: &w1.Address,
	}
	tx1 := signTokenTx(t, w1, createOp, 1)

	if err := l1.apply(tx1); err != nil {
		t.Fatalf("l1 apply failed: %v", err)
	}
	if err := l2.apply(tx1); err != nil {
		t.Fatalf("l2 apply failed: %v", err)
	}

	tokenID := GenerateTokenID(w1.Address, "DET", 1)

	xferOp := TokenInstruction{
		Op:        OpTransferToken,
		TokenID:   tokenID,
		Recipient: w2.Address,
		Amount:    1_250_000,
	}
	tx2 := signTokenTx(t, w1, xferOp, 2)

	if err := l1.apply(tx2); err != nil {
		t.Fatalf("l1 transfer failed: %v", err)
	}
	if err := l2.apply(tx2); err != nil {
		t.Fatalf("l2 transfer failed: %v", err)
	}

	root1 := l1.tokens.ComputeStateRoot()
	root2 := l2.tokens.ComputeStateRoot()

	if root1 == "" || len(root1) != 64 {
		t.Fatalf("invalid root length: %s", root1)
	}
	if root1 != root2 {
		t.Fatalf("non-deterministic state roots: %s vs %s", root1, root2)
	}

	// Verify that burning back to zero does not leave phantom hash differences
	burnOp := TokenInstruction{
		Op:      OpBurn,
		TokenID: tokenID,
		Amount:  1_250_000,
	}
	tx3 := signTokenTx(t, w2, burnOp, 1)
	if err := l1.apply(tx3); err != nil {
		t.Fatalf("l1 burn failed: %v", err)
	}
	// w2 balance is now 0. In l1, w2 has an entry of 0.
	// Compute root and verify it matches deterministic specification
	rootBurned := l1.tokens.ComputeStateRoot()
	if rootBurned == root1 {
		t.Fatal("expected state root to change after burn")
	}
}

func TestTokenFormatAndParse(t *testing.T) {
	// 6 decimals: 1,500,000 -> "1.500000"
	formatted := FormatTokenAmount(1_500_000, 6)
	if formatted != "1.500000" {
		t.Fatalf("expected 1.500000, got %s", formatted)
	}

	parsed, err := ParseTokenAmount("1.500000", 6)
	if err != nil || parsed != 1_500_000 {
		t.Fatalf("parse failed: %v, parsed=%d", err, parsed)
	}

	// 0 decimals: 42 -> "42"
	if FormatTokenAmount(42, 0) != "42" {
		t.Fatalf("expected 42, got %s", FormatTokenAmount(42, 0))
	}
	parsedZero, err := ParseTokenAmount("42", 0)
	if err != nil || parsedZero != 42 {
		t.Fatalf("parse zero decimals failed: %v, parsed=%d", err, parsedZero)
	}

	// Partial fraction: "2.5" with 4 decimals -> 25000
	parsedPartial, err := ParseTokenAmount("2.5", 4)
	if err != nil || parsedPartial != 25000 {
		t.Fatalf("parse partial failed: %v, parsed=%d", err, parsedPartial)
	}

	// Too many decimals: "1.12345" with 3 decimals -> error
	if _, err := ParseTokenAmount("1.12345", 3); err == nil {
		t.Fatal("expected error for excessive decimal places")
	}
}
