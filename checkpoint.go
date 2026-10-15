package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

const CheckpointMaxAge int64 = 24 * 60 * 60

// The public key is trusted out of band, never taken from a peer/certificate.
// Certificates are operator inputs; peers cannot rotate them automatically.
type Checkpoint struct {
	ChainID   string `json:"chain_id"`
	Height    int    `json:"height"`
	Hash      string `json:"hash"`
	IssuedAt  int64  `json:"issued_at"`
	Signature string `json:"signature"`
}

func (c Checkpoint) signingBytes() []byte {
	b, _ := json.Marshal(struct {
		Domain   string `json:"domain"`
		Chain    string `json:"chain_id"`
		Height   int    `json:"height"`
		Hash     string `json:"hash"`
		IssuedAt int64  `json:"issued_at"`
	}{"gochain-checkpoint-v1", c.ChainID, c.Height, c.Hash, c.IssuedAt})
	return b
}

func loadCheckpoint(path, authority string) (*Checkpoint, error) {
	if path == "" || authority == "" {
		return nil, fmt.Errorf("checkpoint file and trusted authority public key are both required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > 4096 {
		return nil, fmt.Errorf("checkpoint file exceeds 4096 bytes")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Checkpoint
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if err := c.verify(authority); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c Checkpoint) verify(authority string) error {
	pub, err := hex.DecodeString(authority)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid checkpoint authority public key")
	}
	hash, err := hex.DecodeString(c.Hash)
	if c.ChainID != ChainID || c.Height < 0 || c.IssuedAt < 0 || err != nil || len(hash) != 32 || hex.EncodeToString(hash) != c.Hash {
		return fmt.Errorf("invalid checkpoint identity")
	}
	sig, err := hex.DecodeString(c.Signature)
	if err != nil || !ed25519.Verify(pub, c.signingBytes(), sig) {
		return fmt.Errorf("invalid checkpoint signature")
	}
	return nil
}

// Historical verification stays possible after expiry. New work fails closed
// until an operator installs a fresh signed anchor, including at every halving.
func (c Checkpoint) check(s chainState, now int64, live bool) error {
	if c.ChainID != s.ChainID || c.Height < 0 || c.Height >= len(s.Blocks) || s.Blocks[c.Height] == nil || s.Blocks[c.Height].Hash != c.Hash {
		return fmt.Errorf("history conflicts with trusted checkpoint")
	}
	if c.IssuedAt > now+MaxFutureSeconds || s.Blocks[c.Height].Timestamp > c.IssuedAt {
		return fmt.Errorf("invalid checkpoint time")
	}
	for _, b := range s.Blocks[c.Height+1:] {
		if b == nil || b.Timestamp < c.IssuedAt {
			return fmt.Errorf("block predates trusted checkpoint time")
		}
	}
	if live && (now-c.IssuedAt > CheckpointMaxAge || rewardAt(now) != rewardAt(c.IssuedAt)) {
		return fmt.Errorf("checkpoint stale or halving crossed; install a fresh signed checkpoint before new work")
	}
	return nil
}

func (bc *Blockchain) checkCheckpoint(s chainState, live bool) error {
	if bc.checkpoint == nil {
		return nil
	}
	return bc.checkpoint.check(s, time.Now().Unix(), live)
}

func runCheckpoint(args []string) error {
	if len(args) == 0 || args[0] != "sign" {
		return fmt.Errorf("usage: checkpoint sign --wallet AUTHORITY_WALLET --out NEW_FILE [--data LEDGER | --genesis]")
	}
	if !CheckpointRequired {
		return fmt.Errorf("checkpoint signing requires the separate -tags checkpoint build")
	}
	fs := flags("checkpoint sign")
	wallet := fs.String("wallet", "", "dedicated authority wallet (private)")
	output := fs.String("out", "", "new public checkpoint file (never overwritten)")
	data := fs.String("data", "", "existing ledger to anchor")
	genesis := fs.Bool("genesis", false, "anchor deterministic genesis for initial startup")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *wallet == "" || *output == "" || (*genesis == (*data != "")) {
		return fmt.Errorf("provide --wallet, --out and exactly one of --data or --genesis")
	}
	w, err := LoadWallet(*wallet)
	if err != nil {
		return err
	}
	s := NewBlockchain().Snapshot()
	if !*genesis {
		b, err := os.ReadFile(*data)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	}
	if _, err := validateState(s, false); err != nil {
		return err
	}
	tip := s.Blocks[len(s.Blocks)-1]
	c := Checkpoint{ChainID: ChainID, Height: tip.Index, Hash: tip.Hash, IssuedAt: time.Now().Unix()}
	if tip.Timestamp > c.IssuedAt {
		return fmt.Errorf("tip is in the future; wait before signing")
	}
	key, err := w.privateKey()
	if err != nil {
		return err
	}
	c.Signature = hex.EncodeToString(ed25519.Sign(key, c.signingBytes()))
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err != nil {
		f.Close()
		os.Remove(*output)
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return printJSON(map[string]any{"checkpoint": c, "authority_public_key": w.PublicKey})
}
