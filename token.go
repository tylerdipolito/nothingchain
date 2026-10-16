package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
)

// GRC-20 Native Token Standard Constants
const (
	GRC20MemoPrefix       = "grc20:"
	MaxTokenNameLength    = 64
	MaxTokenSymbolLength  = 10
	MinTokenSymbolLength  = 1
	MaxTokenDecimals      = 18
	TokenCreationFeeAtoms = 100_000_000 // 1 GO base unit creation fee
)

// TokenInstructionType defines the 5 core GRC-20 instructions.
type TokenInstructionType string

const (
	OpCreateToken   TokenInstructionType = "create_token"
	OpMint          TokenInstructionType = "mint"
	OpTransferToken TokenInstructionType = "transfer_token"
	OpBurn          TokenInstructionType = "burn"
	OpSetAuthority  TokenInstructionType = "set_authority"
)

// TokenMetadata defines the canonical on-chain parameters of a GRC-20 token asset.
type TokenMetadata struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Symbol          string `json:"symbol"`
	Decimals        uint8  `json:"decimals"`
	MaxSupply       int64  `json:"max_supply"` // 0 = unlimited (up to math.MaxInt64)
	TotalSupply     int64  `json:"total_supply"`
	MintAuthority   string `json:"mint_authority"`   // Empty string = revoked permanently
	FreezeAuthority string `json:"freeze_authority"` // Empty string = no freeze authority
	CreatedAt       int64  `json:"created_at"`
	Creator         string `json:"creator"`
}

// TokenInstruction represents the structured payload embedded in a transaction memo.
type TokenInstruction struct {
	Op              TokenInstructionType `json:"op"`
	TokenID         string               `json:"token_id,omitempty"`
	Name            string               `json:"name,omitempty"`
	Symbol          string               `json:"symbol,omitempty"`
	Decimals        uint8                `json:"decimals,omitempty"`
	InitialSupply   int64                `json:"initial_supply,omitempty"`
	MaxSupply       int64                `json:"max_supply,omitempty"`
	MintAuthority   *string              `json:"mint_authority,omitempty"`
	FreezeAuthority *string              `json:"freeze_authority,omitempty"`
	Recipient       string               `json:"recipient,omitempty"`
	Amount          int64                `json:"amount,omitempty"`
	AuthorityType   string               `json:"authority_type,omitempty"` // "mint" or "freeze"
	NewAuthority    *string              `json:"new_authority,omitempty"` // nil/empty to revoke
}

// tokenState stores all active tokens, account balances, and freeze states in the ledger.
type tokenState struct {
	Tokens   map[string]*TokenMetadata   `json:"tokens"`   // token_id -> TokenMetadata
	Balances map[string]map[string]int64 `json:"balances"` // token_id -> address -> balance
	Frozen   map[string]map[string]bool  `json:"frozen"`   // token_id -> address -> is_frozen
}

func newTokenState() *tokenState {
	return &tokenState{
		Tokens:   make(map[string]*TokenMetadata),
		Balances: make(map[string]map[string]int64),
		Frozen:   make(map[string]map[string]bool),
	}
}

func (ts *tokenState) copy() *tokenState {
	cp := newTokenState()
	for k, v := range ts.Tokens {
		metaCopy := *v
		cp.Tokens[k] = &metaCopy
	}
	for tid, bmap := range ts.Balances {
		cp.Balances[tid] = make(map[string]int64, len(bmap))
		for addr, bal := range bmap {
			cp.Balances[tid][addr] = bal
		}
	}
	for tid, fmap := range ts.Frozen {
		cp.Frozen[tid] = make(map[string]bool, len(fmap))
		for addr, fr := range fmap {
			cp.Frozen[tid][addr] = fr
		}
	}
	return cp
}

// GenerateTokenID computes a deterministic, unique Asset ID for a token.
func GenerateTokenID(creator, symbol string, nonce uint64) string {
	cleanSym := strings.ToLower(strings.TrimSpace(symbol))
	payload := fmt.Sprintf("%s:%s:%d", creator, cleanSym, nonce)
	h := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("grc20:%s:%s", cleanSym, hex.EncodeToString(h[:4]))
}

// ValidateSymbol checks that a token symbol is uppercase alphanumeric and within bounds.
func ValidateSymbol(sym string) bool {
	if len(sym) < MinTokenSymbolLength || len(sym) > MaxTokenSymbolLength {
		return false
	}
	for _, r := range sym {
		if !unicode.IsUpper(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// EncodeTokenInstruction encodes a TokenInstruction to canonical JSON memo format.
func EncodeTokenInstruction(op TokenInstruction) (string, error) {
	data, err := json.Marshal(op)
	if err != nil {
		return "", err
	}
	return GRC20MemoPrefix + string(data), nil
}

// FormatTokenAmount formats integer base units into a decimal string based on token decimals.
func FormatTokenAmount(amount int64, decimals uint8) string {
	if decimals == 0 {
		return fmt.Sprintf("%d", amount)
	}
	neg := amount < 0
	if neg {
		amount = -amount
	}
	scale := int64(math.Pow10(int(decimals)))
	whole := amount / scale
	frac := amount % scale
	res := fmt.Sprintf("%d.%0*d", whole, int(decimals), frac)
	if neg {
		res = "-" + res
	}
	return res
}

// ParseTokenAmount converts a decimal string (e.g. "100.5") into base units integer according to decimals.
func ParseTokenAmount(s string, decimals uint8) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("amount is empty")
	}
	parts := strings.Split(s, ".")
	if len(parts) > 2 {
		return 0, fmt.Errorf("multiple decimal points in amount")
	}
	var whole, frac int64
	var err error
	if parts[0] != "" {
		if _, err = fmt.Sscan(parts[0], &whole); err != nil {
			return 0, fmt.Errorf("invalid whole amount: %w", err)
		}
	}
	if len(parts) == 2 {
		fracStr := parts[1]
		if len(fracStr) > int(decimals) {
			return 0, fmt.Errorf("too many decimal places (max %d)", decimals)
		}
		fracStr = fracStr + strings.Repeat("0", int(decimals)-len(fracStr))
		if _, err = fmt.Sscan(fracStr, &frac); err != nil {
			return 0, fmt.Errorf("invalid fractional amount: %w", err)
		}
	}
	scale := int64(math.Pow10(int(decimals)))
	if whole > math.MaxInt64/scale {
		return 0, fmt.Errorf("amount overflow")
	}
	return whole*scale + frac, nil
}

// ComputeStateRoot calculates a deterministic 32-byte SHA-256 state root over all token state.
func (ts *tokenState) ComputeStateRoot() string {
	var tokenIDs []string
	for tid := range ts.Tokens {
		tokenIDs = append(tokenIDs, tid)
	}
	sort.Strings(tokenIDs)

	h := sha256.New()
	for _, tid := range tokenIDs {
		t := ts.Tokens[tid]
		tokenHeader := fmt.Sprintf("%s|%s|%s|%d|%d|%d|%s|%s|%d|%s\n",
			t.ID, t.Name, t.Symbol, t.Decimals, t.MaxSupply, t.TotalSupply,
			t.MintAuthority, t.FreezeAuthority, t.CreatedAt, t.Creator)
		h.Write([]byte(tokenHeader))

		// Sort account balances for this token
		var accounts []string
		if bmap, ok := ts.Balances[tid]; ok {
			for addr, bal := range bmap {
				if bal > 0 {
					accounts = append(accounts, addr)
				}
			}
		}
		sort.Strings(accounts)
		for _, addr := range accounts {
			balLine := fmt.Sprintf("%s:%d\n", addr, ts.Balances[tid][addr])
			h.Write([]byte(balLine))
		}

		// Sort frozen accounts for this token
		var frozenAccounts []string
		if fmap, ok := ts.Frozen[tid]; ok {
			for addr, fr := range fmap {
				if fr {
					frozenAccounts = append(frozenAccounts, addr)
				}
			}
		}
		sort.Strings(frozenAccounts)
		for _, addr := range frozenAccounts {
			frLine := fmt.Sprintf("frozen:%s\n", addr)
			h.Write([]byte(frLine))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// applyGRC20 executes a GRC-20 token instruction within the ledger state machine.
func (l *ledger) applyGRC20(tx Transaction) error {
	if !strings.HasPrefix(tx.Memo, GRC20MemoPrefix) {
		return fmt.Errorf("invalid token memo format")
	}

	rawJSON := strings.TrimPrefix(tx.Memo, GRC20MemoPrefix)
	var op TokenInstruction
	dec := json.NewDecoder(strings.NewReader(rawJSON))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&op); err != nil {
		return fmt.Errorf("malformed token instruction JSON: %w", err)
	}

	// Canonical serialization check (anti-malleability)
	canonical, err := json.Marshal(op)
	if err != nil || GRC20MemoPrefix+string(canonical) != tx.Memo {
		return fmt.Errorf("non-canonical token instruction encoding")
	}

	// Initialize token state if nil
	if l.tokens == nil {
		l.tokens = newTokenState()
	}

	switch op.Op {
	case OpCreateToken:
		return l.applyCreateToken(tx, op)
	case OpMint:
		return l.applyMint(tx, op)
	case OpTransferToken:
		return l.applyTransferToken(tx, op)
	case OpBurn:
		return l.applyBurn(tx, op)
	case OpSetAuthority:
		return l.applySetAuthority(tx, op)
	default:
		return fmt.Errorf("unknown token instruction: %s", op.Op)
	}
}

func (l *ledger) applyCreateToken(tx Transaction, op TokenInstruction) error {
	if tx.Amount != 0 || tx.To != tx.From {
		return fmt.Errorf("create_token requires zero-amount self-transfer (use GO balance for fee)")
	}

	// Deduct and permanently burn native token creation fee in GO (1 GO = 100,000,000 atoms)
	if l.balances[tx.From] < TokenCreationFeeAtoms {
		return fmt.Errorf("insufficient GO balance to pay token creation fee (requires %.2f GO)", float64(TokenCreationFeeAtoms)/float64(UnitsPerGO))
	}
	l.balances[tx.From] -= TokenCreationFeeAtoms
	l.supply -= TokenCreationFeeAtoms
	l.burned += TokenCreationFeeAtoms

	op.Name = strings.TrimSpace(op.Name)
	if len(op.Name) == 0 || len(op.Name) > MaxTokenNameLength {
		return fmt.Errorf("token name must be between 1 and %d characters", MaxTokenNameLength)
	}
	if !ValidateSymbol(op.Symbol) {
		return fmt.Errorf("token symbol must be 1-%d uppercase alphanumeric characters", MaxTokenSymbolLength)
	}
	if op.Decimals > MaxTokenDecimals {
		return fmt.Errorf("token decimals cannot exceed %d", MaxTokenDecimals)
	}
	if op.InitialSupply < 0 {
		return fmt.Errorf("initial supply cannot be negative")
	}
	if op.MaxSupply < 0 {
		return fmt.Errorf("max supply cannot be negative")
	}
	if op.MaxSupply > 0 && op.InitialSupply > op.MaxSupply {
		return fmt.Errorf("initial supply exceeds max supply")
	}

	tokenID := GenerateTokenID(tx.From, op.Symbol, tx.Nonce)
	if _, exists := l.tokens.Tokens[tokenID]; exists {
		return fmt.Errorf("token ID %s already exists", tokenID)
	}

	mintAuth := tx.From
	if op.MintAuthority != nil {
		mintAuth = strings.TrimSpace(*op.MintAuthority)
		if mintAuth != "" && !validAddress(mintAuth) {
			return fmt.Errorf("invalid mint authority address")
		}
	}

	freezeAuth := ""
	if op.FreezeAuthority != nil {
		freezeAuth = strings.TrimSpace(*op.FreezeAuthority)
		if freezeAuth != "" && !validAddress(freezeAuth) {
			return fmt.Errorf("invalid freeze authority address")
		}
	}

	meta := &TokenMetadata{
		ID:              tokenID,
		Name:            op.Name,
		Symbol:          op.Symbol,
		Decimals:        op.Decimals,
		MaxSupply:       op.MaxSupply,
		TotalSupply:     op.InitialSupply,
		MintAuthority:   mintAuth,
		FreezeAuthority: freezeAuth,
		CreatedAt:       l.height,
		Creator:         tx.From,
	}

	l.tokens.Tokens[tokenID] = meta
	l.tokens.Balances[tokenID] = make(map[string]int64)
	l.tokens.Frozen[tokenID] = make(map[string]bool)

	if op.InitialSupply > 0 {
		l.tokens.Balances[tokenID][tx.From] = op.InitialSupply
	}

	return nil
}

func (l *ledger) applyMint(tx Transaction, op TokenInstruction) error {
	if tx.Amount != 0 || tx.To != tx.From {
		return fmt.Errorf("mint instruction requires zero-amount self-transfer")
	}

	meta, exists := l.tokens.Tokens[op.TokenID]
	if !exists {
		return fmt.Errorf("token %s does not exist", op.TokenID)
	}

	if meta.MintAuthority == "" {
		return fmt.Errorf("mint authority for token %s has been permanently revoked", op.TokenID)
	}
	if tx.From != meta.MintAuthority {
		return fmt.Errorf("sender %s is not the authorized mint authority (%s)", tx.From, meta.MintAuthority)
	}
	if op.Amount <= 0 {
		return fmt.Errorf("mint amount must be strictly positive")
	}
	if !validAddress(op.Recipient) {
		return fmt.Errorf("invalid recipient address")
	}

	// Max supply & integer overflow check
	if op.Amount > math.MaxInt64-meta.TotalSupply {
		return fmt.Errorf("mint amount causes total supply overflow")
	}
	newTotal := meta.TotalSupply + op.Amount
	if meta.MaxSupply > 0 && newTotal > meta.MaxSupply {
		return fmt.Errorf("mint amount would exceed max supply of %d base units", meta.MaxSupply)
	}

	currentBal := l.tokens.Balances[op.TokenID][op.Recipient]
	if op.Amount > math.MaxInt64-currentBal {
		return fmt.Errorf("recipient balance overflow")
	}

	meta.TotalSupply = newTotal
	l.tokens.Balances[op.TokenID][op.Recipient] = currentBal + op.Amount
	return nil
}

func (l *ledger) applyTransferToken(tx Transaction, op TokenInstruction) error {
	if tx.Amount != 0 || tx.To != tx.From {
		return fmt.Errorf("transfer_token instruction requires zero-amount self-transfer")
	}

	meta, exists := l.tokens.Tokens[op.TokenID]
	if !exists {
		return fmt.Errorf("token %s does not exist", op.TokenID)
	}
	_ = meta

	if op.Amount <= 0 {
		return fmt.Errorf("transfer amount must be strictly positive")
	}
	if !validAddress(op.Recipient) {
		return fmt.Errorf("invalid recipient address")
	}

	// Freeze check
	if l.tokens.Frozen[op.TokenID][tx.From] {
		return fmt.Errorf("sender wallet is frozen for token %s", op.TokenID)
	}
	if l.tokens.Frozen[op.TokenID][op.Recipient] {
		return fmt.Errorf("recipient wallet is frozen for token %s", op.TokenID)
	}

	senderBal := l.tokens.Balances[op.TokenID][tx.From]
	if senderBal < op.Amount {
		return fmt.Errorf("insufficient token balance: have %d base units, need %d", senderBal, op.Amount)
	}

	if tx.From != op.Recipient {
		recipientBal := l.tokens.Balances[op.TokenID][op.Recipient]
		if op.Amount > math.MaxInt64-recipientBal {
			return fmt.Errorf("recipient token balance overflow")
		}
		l.tokens.Balances[op.TokenID][tx.From] -= op.Amount
		l.tokens.Balances[op.TokenID][op.Recipient] += op.Amount
	}

	return nil
}

func (l *ledger) applyBurn(tx Transaction, op TokenInstruction) error {
	if tx.Amount != 0 || tx.To != tx.From {
		return fmt.Errorf("burn instruction requires zero-amount self-transfer")
	}

	meta, exists := l.tokens.Tokens[op.TokenID]
	if !exists {
		return fmt.Errorf("token %s does not exist", op.TokenID)
	}

	if op.Amount <= 0 {
		return fmt.Errorf("burn amount must be strictly positive")
	}

	senderBal := l.tokens.Balances[op.TokenID][tx.From]
	if senderBal < op.Amount {
		return fmt.Errorf("insufficient token balance to burn: have %d base units, need %d", senderBal, op.Amount)
	}

	l.tokens.Balances[op.TokenID][tx.From] -= op.Amount
	meta.TotalSupply -= op.Amount
	return nil
}

func (l *ledger) applySetAuthority(tx Transaction, op TokenInstruction) error {
	if tx.Amount != 0 || tx.To != tx.From {
		return fmt.Errorf("set_authority instruction requires zero-amount self-transfer")
	}

	meta, exists := l.tokens.Tokens[op.TokenID]
	if !exists {
		return fmt.Errorf("token %s does not exist", op.TokenID)
	}

	newAuth := ""
	if op.NewAuthority != nil {
		newAuth = strings.TrimSpace(*op.NewAuthority)
		if newAuth != "" && !validAddress(newAuth) {
			return fmt.Errorf("invalid new authority address")
		}
	}

	switch strings.ToLower(op.AuthorityType) {
	case "mint":
		if meta.MintAuthority == "" {
			return fmt.Errorf("mint authority has already been permanently revoked")
		}
		if tx.From != meta.MintAuthority {
			return fmt.Errorf("only current mint authority (%s) can transfer or revoke", meta.MintAuthority)
		}
		meta.MintAuthority = newAuth
	case "freeze":
		if meta.FreezeAuthority == "" {
			return fmt.Errorf("freeze authority has already been permanently revoked")
		}
		if tx.From != meta.FreezeAuthority {
			return fmt.Errorf("only current freeze authority (%s) can transfer or revoke", meta.FreezeAuthority)
		}
		meta.FreezeAuthority = newAuth
	default:
		return fmt.Errorf("unknown authority type: %s (must be 'mint' or 'freeze')", op.AuthorityType)
	}

	return nil
}
