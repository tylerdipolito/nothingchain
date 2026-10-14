package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Wallet struct {
	Address   string `json:"address"`
	PublicKey string `json:"public_key"`
	Seed      string `json:"seed"`
}

func addressFor(pub []byte) string {
	sum := sha256.Sum256(pub)
	return "go1" + hex.EncodeToString(sum[:])
}
func validAddress(addr string) bool {
	if len(addr) != 67 || !strings.HasPrefix(addr, "go1") || addr != strings.ToLower(addr) {
		return false
	}
	_, err := hex.DecodeString(addr[3:])
	return err == nil
}

func CreateWallet(path string) (*Wallet, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	w := &Wallet{Address: addressFor(pub), PublicKey: hex.EncodeToString(pub), Seed: hex.EncodeToString(priv.Seed())}
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("create wallet (will not overwrite an existing file): %w", err)
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	return w, nil
}

func LoadWallet(path string) (*Wallet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var w Wallet
	if err = json.Unmarshal(data, &w); err != nil {
		return nil, err
	}
	priv, err := w.privateKey()
	if err != nil {
		return nil, err
	}
	pub := priv.Public().(ed25519.PublicKey)
	if w.Address != addressFor(pub) || w.PublicKey != hex.EncodeToString(pub) {
		return nil, fmt.Errorf("wallet key and address do not match")
	}
	return &w, nil
}
func (w *Wallet) privateKey() (ed25519.PrivateKey, error) {
	seed, err := hex.DecodeString(w.Seed)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("invalid wallet seed")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
func (w *Wallet) Sign(to string, amount int64, memo string, nonce uint64) (Transaction, error) {
	priv, err := w.privateKey()
	if err != nil {
		return Transaction{}, err
	}
	tx := Transaction{From: w.Address, To: to, Amount: amount, Memo: memo, Nonce: nonce, PublicKey: w.PublicKey}
	tx.Signature = hex.EncodeToString(ed25519.Sign(priv, tx.signingBytes()))
	return tx, tx.Validate()
}
