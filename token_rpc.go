package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

// TokenRPCPayload handles requests for token instructions via HTTP.
type TokenRPCPayload struct {
	Tx              *Transaction `json:"tx,omitempty"`
	Wallet          string       `json:"wallet,omitempty"`
	PrivateKey      string       `json:"private_key,omitempty"`
	Name            string       `json:"name,omitempty"`
	Symbol          string       `json:"symbol,omitempty"`
	Decimals        uint8        `json:"decimals,omitempty"`
	InitialSupply   int64        `json:"initial_supply,omitempty"`
	MaxSupply       int64        `json:"max_supply,omitempty"`
	MintAuthority   *string      `json:"mint_authority,omitempty"`
	FreezeAuthority *string      `json:"freeze_authority,omitempty"`
	TokenID         string       `json:"token_id,omitempty"`
	Recipient       string       `json:"recipient,omitempty"`
	Amount          int64        `json:"amount,omitempty"`
	AuthorityType   string       `json:"authority_type,omitempty"`
	NewAuthority    *string      `json:"new_authority,omitempty"`
}

func loadWalletFromPayload(p *TokenRPCPayload) (*Wallet, error) {
	if p.PrivateKey != "" {
		seed, err := hex.DecodeString(strings.TrimSpace(p.PrivateKey))
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("invalid private_key (must be 64-char hex ed25519 seed)")
		}
		priv := ed25519.NewKeyFromSeed(seed)
		pub := priv.Public().(ed25519.PublicKey)
		return &Wallet{
			Address:   addressFor(pub),
			PublicKey: hex.EncodeToString(pub),
			Seed:      hex.EncodeToString(seed),
		}, nil
	}
	if p.Wallet != "" {
		return LoadWallet(p.Wallet)
	}
	return nil, nil
}

func registerTokens(mux *http.ServeMux, bc *Blockchain) {
	// Query: List all active tokens
	mux.HandleFunc("GET /token/list", func(w http.ResponseWriter, r *http.Request) {
		tokens, err := bc.Tokens()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{
			"count":  len(tokens),
			"tokens": tokens,
		})
	})

	// Query: Get metadata for a specific token
	mux.HandleFunc("GET /token/metadata/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/token/metadata/")
		if id == "" {
			id = r.URL.Query().Get("id")
		}
		if id == "" {
			http.Error(w, "missing token id", http.StatusBadRequest)
			return
		}
		meta, err := bc.TokenMetadata(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, meta)
	})

	// Query: Get balance of an address for a specific token
	mux.HandleFunc("GET /token/balance/", func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimPrefix(r.URL.Path, "/token/balance/")
		parts := strings.Split(strings.Trim(raw, "/"), "/")
		var tokenID, address string
		if len(parts) >= 2 {
			tokenID, address = parts[0], parts[1]
		} else {
			tokenID = r.URL.Query().Get("token_id")
			address = r.URL.Query().Get("address")
		}
		if tokenID == "" || address == "" {
			http.Error(w, "token_id and address required (format: /token/balance/{token_id}/{address})", http.StatusBadRequest)
			return
		}
		meta, err := bc.TokenMetadata(tokenID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		bal, err := bc.TokenBalance(tokenID, address)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{
			"token_id":  tokenID,
			"name":      meta.Name,
			"symbol":    meta.Symbol,
			"address":   address,
			"balance":   bal,
			"decimals":  meta.Decimals,
			"formatted": FormatTokenAmount(bal, meta.Decimals),
		})
	})

	// Query: Get all token holders
	mux.HandleFunc("GET /token/holders/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/token/holders/")
		if id == "" {
			id = r.URL.Query().Get("token_id")
		}
		if id == "" {
			http.Error(w, "missing token id", http.StatusBadRequest)
			return
		}
		meta, err := bc.TokenMetadata(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		holders, err := bc.TokenHolders(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{
			"token_id":         id,
			"name":             meta.Name,
			"symbol":           meta.Symbol,
			"total_supply":     meta.TotalSupply,
			"formatted_supply": FormatTokenAmount(meta.TotalSupply, meta.Decimals),
			"count":            len(holders),
			"holders":          holders,
		})
	})

	// Query: Get canonical token state root hash
	mux.HandleFunc("GET /token/state-root", func(w http.ResponseWriter, r *http.Request) {
		root, err := bc.TokenStateRoot()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{
			"state_root": root,
			"height":     bc.Stats().Height,
		})
	})

	// Mutation: POST /token/create
	mux.HandleFunc("POST /token/create", func(w http.ResponseWriter, r *http.Request) {
		var req TokenRPCPayload
		if err := decodeBody(w, r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Tx != nil {
			if err := bc.AddTx(*req.Tx); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"status": "queued", "tx": req.Tx})
			return
		}
		signer, err := loadWalletFromPayload(&req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		op := TokenInstruction{
			Op:              OpCreateToken,
			Name:            req.Name,
			Symbol:          req.Symbol,
			Decimals:        req.Decimals,
			InitialSupply:   req.InitialSupply,
			MaxSupply:       req.MaxSupply,
			MintAuthority:   req.MintAuthority,
			FreezeAuthority: req.FreezeAuthority,
		}
		memo, err := EncodeTokenInstruction(op)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if signer == nil {
			writeJSON(w, map[string]any{
				"status":      "prepared",
				"memo":        memo,
				"instruction": op,
				"fee_atoms":   TokenCreationFeeAtoms,
			})
			return
		}
		acct, err := bc.Account(signer.Address)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		tokenID := GenerateTokenID(signer.Address, req.Symbol, acct.NextNonce)
		tx, err := signer.Sign(signer.Address, 0, memo, acct.NextNonce)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := bc.AddTx(tx); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{
			"status":   "queued",
			"token_id": tokenID,
			"tx":       tx,
		})
	})

	// Mutation: POST /token/mint
	mux.HandleFunc("POST /token/mint", func(w http.ResponseWriter, r *http.Request) {
		var req TokenRPCPayload
		if err := decodeBody(w, r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Tx != nil {
			if err := bc.AddTx(*req.Tx); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"status": "queued", "tx": req.Tx})
			return
		}
		signer, err := loadWalletFromPayload(&req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		op := TokenInstruction{
			Op:        OpMint,
			TokenID:   req.TokenID,
			Recipient: req.Recipient,
			Amount:    req.Amount,
		}
		memo, err := EncodeTokenInstruction(op)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if signer == nil {
			writeJSON(w, map[string]any{"status": "prepared", "memo": memo, "instruction": op})
			return
		}
		acct, err := bc.Account(signer.Address)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		tx, err := signer.Sign(signer.Address, 0, memo, acct.NextNonce)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := bc.AddTx(tx); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"status": "queued", "tx": tx})
	})

	// Mutation: POST /token/transfer
	mux.HandleFunc("POST /token/transfer", func(w http.ResponseWriter, r *http.Request) {
		var req TokenRPCPayload
		if err := decodeBody(w, r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Tx != nil {
			if err := bc.AddTx(*req.Tx); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"status": "queued", "tx": req.Tx})
			return
		}
		signer, err := loadWalletFromPayload(&req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		op := TokenInstruction{
			Op:        OpTransferToken,
			TokenID:   req.TokenID,
			Recipient: req.Recipient,
			Amount:    req.Amount,
		}
		memo, err := EncodeTokenInstruction(op)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if signer == nil {
			writeJSON(w, map[string]any{"status": "prepared", "memo": memo, "instruction": op})
			return
		}
		acct, err := bc.Account(signer.Address)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		tx, err := signer.Sign(signer.Address, 0, memo, acct.NextNonce)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := bc.AddTx(tx); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"status": "queued", "tx": tx})
	})

	// Mutation: POST /token/burn
	mux.HandleFunc("POST /token/burn", func(w http.ResponseWriter, r *http.Request) {
		var req TokenRPCPayload
		if err := decodeBody(w, r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Tx != nil {
			if err := bc.AddTx(*req.Tx); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"status": "queued", "tx": req.Tx})
			return
		}
		signer, err := loadWalletFromPayload(&req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		op := TokenInstruction{
			Op:      OpBurn,
			TokenID: req.TokenID,
			Amount:  req.Amount,
		}
		memo, err := EncodeTokenInstruction(op)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if signer == nil {
			writeJSON(w, map[string]any{"status": "prepared", "memo": memo, "instruction": op})
			return
		}
		acct, err := bc.Account(signer.Address)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		tx, err := signer.Sign(signer.Address, 0, memo, acct.NextNonce)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := bc.AddTx(tx); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"status": "queued", "tx": tx})
	})

	// Mutation: POST /token/set-authority
	mux.HandleFunc("POST /token/set-authority", func(w http.ResponseWriter, r *http.Request) {
		var req TokenRPCPayload
		if err := decodeBody(w, r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Tx != nil {
			if err := bc.AddTx(*req.Tx); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"status": "queued", "tx": req.Tx})
			return
		}
		signer, err := loadWalletFromPayload(&req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		op := TokenInstruction{
			Op:            OpSetAuthority,
			TokenID:       req.TokenID,
			AuthorityType: req.AuthorityType,
			NewAuthority:  req.NewAuthority,
		}
		memo, err := EncodeTokenInstruction(op)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if signer == nil {
			writeJSON(w, map[string]any{"status": "prepared", "memo": memo, "instruction": op})
			return
		}
		acct, err := bc.Account(signer.Address)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		tx, err := signer.Sign(signer.Address, 0, memo, acct.NextNonce)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := bc.AddTx(tx); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"status": "queued", "tx": tx})
	})
}

// runToken implements the CLI interface for GRC-20 token commands.
func runToken(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gochain token create|mint|transfer|burn|set-authority|list|info|balance|holders|state-root [flags]")
	}
	switch args[0] {
	case "create":
		fs := flags("token create")
		name := fs.String("name", "", "token name (e.g. 'Coffee Bucks')")
		symbol := fs.String("symbol", "", "token symbol (e.g. 'BREW')")
		decimals := fs.Uint("decimals", 6, "token decimals (0-18)")
		initialSupply := fs.Int64("initial", 0, "initial supply in base units")
		maxSupply := fs.Int64("max", 0, "max supply in base units (0 = unlimited)")
		mintAuth := fs.String("mint-auth", "", "mint authority address (defaults to creator)")
		freezeAuth := fs.String("freeze-auth", "", "freeze authority address (defaults to none)")
		walletPath := fs.String("wallet", WalletFile, "wallet file")
		node := fs.String("node", DefaultNode, "node URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" || *symbol == "" {
			return fmt.Errorf("--name and --symbol are required")
		}
		w, err := LoadWallet(*walletPath)
		if err != nil {
			return err
		}
		var acct Account
		if err := request(*node, "/balance/"+w.Address, nil, &acct); err != nil {
			return err
		}
		mAuth := w.Address
		if *mintAuth != "" {
			mAuth = *mintAuth
		}
		var fAuth *string
		if *freezeAuth != "" {
			fAuth = freezeAuth
		}
		op := TokenInstruction{
			Op:              OpCreateToken,
			Name:            *name,
			Symbol:          *symbol,
			Decimals:        uint8(*decimals),
			InitialSupply:   *initialSupply,
			MaxSupply:       *maxSupply,
			MintAuthority:   &mAuth,
			FreezeAuthority: fAuth,
		}
		memo, err := EncodeTokenInstruction(op)
		if err != nil {
			return err
		}
		tokenID := GenerateTokenID(w.Address, *symbol, acct.NextNonce)
		tx, err := w.Sign(w.Address, 0, memo, acct.NextNonce)
		if err != nil {
			return err
		}
		var res any
		if err := request(*node, "/tx", tx, &res); err != nil {
			return err
		}
		return printJSON(map[string]any{"token_id": tokenID, "tx": tx, "queued": res})

	case "mint":
		fs := flags("token mint")
		tokenID := fs.String("token", "", "token ID")
		to := fs.String("to", "", "recipient address")
		amount := fs.Int64("amount", 0, "amount in base units")
		walletPath := fs.String("wallet", WalletFile, "wallet file")
		node := fs.String("node", DefaultNode, "node URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *tokenID == "" || *to == "" || *amount <= 0 {
			return fmt.Errorf("--token, --to, and positive --amount are required")
		}
		w, err := LoadWallet(*walletPath)
		if err != nil {
			return err
		}
		var acct Account
		if err := request(*node, "/balance/"+w.Address, nil, &acct); err != nil {
			return err
		}
		op := TokenInstruction{
			Op:        OpMint,
			TokenID:   *tokenID,
			Recipient: *to,
			Amount:    *amount,
		}
		memo, err := EncodeTokenInstruction(op)
		if err != nil {
			return err
		}
		tx, err := w.Sign(w.Address, 0, memo, acct.NextNonce)
		if err != nil {
			return err
		}
		var res any
		if err := request(*node, "/tx", tx, &res); err != nil {
			return err
		}
		return printJSON(map[string]any{"tx": tx, "queued": res})

	case "transfer":
		fs := flags("token transfer")
		tokenID := fs.String("token", "", "token ID")
		to := fs.String("to", "", "recipient address")
		amount := fs.Int64("amount", 0, "amount in base units")
		walletPath := fs.String("wallet", WalletFile, "wallet file")
		node := fs.String("node", DefaultNode, "node URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *tokenID == "" || *to == "" || *amount <= 0 {
			return fmt.Errorf("--token, --to, and positive --amount are required")
		}
		w, err := LoadWallet(*walletPath)
		if err != nil {
			return err
		}
		var acct Account
		if err := request(*node, "/balance/"+w.Address, nil, &acct); err != nil {
			return err
		}
		op := TokenInstruction{
			Op:        OpTransferToken,
			TokenID:   *tokenID,
			Recipient: *to,
			Amount:    *amount,
		}
		memo, err := EncodeTokenInstruction(op)
		if err != nil {
			return err
		}
		tx, err := w.Sign(w.Address, 0, memo, acct.NextNonce)
		if err != nil {
			return err
		}
		var res any
		if err := request(*node, "/tx", tx, &res); err != nil {
			return err
		}
		return printJSON(map[string]any{"tx": tx, "queued": res})

	case "burn":
		fs := flags("token burn")
		tokenID := fs.String("token", "", "token ID")
		amount := fs.Int64("amount", 0, "amount in base units")
		walletPath := fs.String("wallet", WalletFile, "wallet file")
		node := fs.String("node", DefaultNode, "node URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *tokenID == "" || *amount <= 0 {
			return fmt.Errorf("--token and positive --amount are required")
		}
		w, err := LoadWallet(*walletPath)
		if err != nil {
			return err
		}
		var acct Account
		if err := request(*node, "/balance/"+w.Address, nil, &acct); err != nil {
			return err
		}
		op := TokenInstruction{
			Op:      OpBurn,
			TokenID: *tokenID,
			Amount:  *amount,
		}
		memo, err := EncodeTokenInstruction(op)
		if err != nil {
			return err
		}
		tx, err := w.Sign(w.Address, 0, memo, acct.NextNonce)
		if err != nil {
			return err
		}
		var res any
		if err := request(*node, "/tx", tx, &res); err != nil {
			return err
		}
		return printJSON(map[string]any{"tx": tx, "queued": res})

	case "set-authority":
		fs := flags("token set-authority")
		tokenID := fs.String("token", "", "token ID")
		authType := fs.String("type", "mint", "authority type: 'mint' or 'freeze'")
		newAuth := fs.String("new-auth", "", "new authority address (empty to revoke)")
		walletPath := fs.String("wallet", WalletFile, "wallet file")
		node := fs.String("node", DefaultNode, "node URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *tokenID == "" {
			return fmt.Errorf("--token is required")
		}
		w, err := LoadWallet(*walletPath)
		if err != nil {
			return err
		}
		var acct Account
		if err := request(*node, "/balance/"+w.Address, nil, &acct); err != nil {
			return err
		}
		var authPtr *string
		if *newAuth != "" {
			authPtr = newAuth
		}
		op := TokenInstruction{
			Op:            OpSetAuthority,
			TokenID:       *tokenID,
			AuthorityType: *authType,
			NewAuthority:  authPtr,
		}
		memo, err := EncodeTokenInstruction(op)
		if err != nil {
			return err
		}
		tx, err := w.Sign(w.Address, 0, memo, acct.NextNonce)
		if err != nil {
			return err
		}
		var res any
		if err := request(*node, "/tx", tx, &res); err != nil {
			return err
		}
		return printJSON(map[string]any{"tx": tx, "queued": res})

	case "list":
		fs := flags("token list")
		node := fs.String("node", DefaultNode, "node URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var res any
		if err := request(*node, "/token/list", nil, &res); err != nil {
			return err
		}
		return printJSON(res)

	case "info":
		fs := flags("token info")
		tokenID := fs.String("token", "", "token ID")
		node := fs.String("node", DefaultNode, "node URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *tokenID == "" {
			return fmt.Errorf("--token is required")
		}
		var res any
		if err := request(*node, "/token/metadata/"+*tokenID, nil, &res); err != nil {
			return err
		}
		return printJSON(res)

	case "balance":
		fs := flags("token balance")
		tokenID := fs.String("token", "", "token ID")
		addr := fs.String("address", "", "wallet address (defaults to local wallet)")
		walletPath := fs.String("wallet", WalletFile, "wallet file")
		node := fs.String("node", DefaultNode, "node URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *tokenID == "" {
			return fmt.Errorf("--token is required")
		}
		if *addr == "" {
			w, err := LoadWallet(*walletPath)
			if err != nil {
				return err
			}
			*addr = w.Address
		}
		var res any
		if err := request(*node, fmt.Sprintf("/token/balance/%s/%s", *tokenID, *addr), nil, &res); err != nil {
			return err
		}
		return printJSON(res)

	case "holders":
		fs := flags("token holders")
		tokenID := fs.String("token", "", "token ID")
		node := fs.String("node", DefaultNode, "node URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *tokenID == "" {
			return fmt.Errorf("--token is required")
		}
		var res any
		if err := request(*node, "/token/holders/"+*tokenID, nil, &res); err != nil {
			return err
		}
		return printJSON(res)

	case "state-root":
		fs := flags("token state-root")
		node := fs.String("node", DefaultNode, "node URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var res any
		if err := request(*node, "/token/state-root", nil, &res); err != nil {
			return err
		}
		return printJSON(res)

	default:
		return fmt.Errorf("unknown token command: %s", args[0])
	}
}
