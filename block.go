package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const Difficulty = 4
const MaxMemoBytes = 512
const MaxPendingTxs = 1000

// Transaction transfers integer atomic GO units and is authorized by the sender's key.
type Transaction struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Amount    int64  `json:"amount_atoms"`
	Memo      string `json:"memo"`
	Nonce     uint64 `json:"nonce"`
	PublicKey string `json:"public_key"`
	Signature string `json:"signature"`
}

func (tx Transaction) signingBytes() []byte {
	data, _ := json.Marshal(struct {
		Chain  string `json:"chain"`
		From   string `json:"from"`
		To     string `json:"to"`
		Amount int64  `json:"amount_atoms"`
		Memo   string `json:"memo"`
		Nonce  uint64 `json:"nonce"`
	}{ChainID, tx.From, tx.To, tx.Amount, tx.Memo, tx.Nonce})
	return data
}

func (tx Transaction) Validate() error {
	if !validAddress(tx.From) || !validAddress(tx.To) {
		return fmt.Errorf("sender and recipient must be GoChain wallet addresses")
	}
	if tx.Amount < 0 {
		return fmt.Errorf("amount cannot be negative")
	}
	if len(tx.Memo) > MaxMemoBytes {
		return fmt.Errorf("memo exceeds %d bytes", MaxMemoBytes)
	}
	if tx.Nonce == 0 {
		return fmt.Errorf("transaction nonce must start at 1")
	}
	pub, err := hex.DecodeString(tx.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize || addressFor(pub) != tx.From {
		return fmt.Errorf("public key does not match sender")
	}
	sig, err := hex.DecodeString(tx.Signature)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(pub), tx.signingBytes(), sig) {
		return fmt.Errorf("invalid transaction signature")
	}
	return nil
}

type Block struct {
	Index     int           `json:"index"`
	Timestamp int64         `json:"timestamp"`
	Txs       []Transaction `json:"txs"`
	PrevHash  string        `json:"prev_hash"`
	Nonce     int64         `json:"nonce"`
	Miner     string        `json:"miner"`
	Reward    int64         `json:"reward_atoms"`
	Hash      string        `json:"hash"`
	Bits      uint32        `json:"bits,omitempty"`
	AuxPoW    *AuxPoW       `json:"auxpow,omitempty"`
}

func (b *Block) computeHash() string {
	payload, _ := json.Marshal(struct {
		Chain     string        `json:"chain"`
		Index     int           `json:"index"`
		Timestamp int64         `json:"timestamp"`
		Txs       []Transaction `json:"txs"`
		PrevHash  string        `json:"prev_hash"`
		Nonce     int64         `json:"nonce"`
		Miner     string        `json:"miner"`
		Reward    int64         `json:"reward_atoms"`
	}{ChainID, b.Index, b.Timestamp, b.Txs, b.PrevHash, b.Nonce, b.Miner, b.Reward})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (b *Block) Mine(difficulty int) {
	target := strings.Repeat("0", difficulty)
	for {
		b.Hash = b.computeHash()
		if strings.HasPrefix(b.Hash, target) {
			return
		}
		b.Nonce++
	}
}

func NewBlock(index int, prevHash string, txs []Transaction, miner string) *Block {
	if txs == nil {
		txs = []Transaction{}
	}
	return &Block{Index: index, Timestamp: time.Now().Unix(), Txs: txs, PrevHash: prevHash, Miner: miner, Reward: rewardAt(time.Now().Unix())}
}

func GenesisBlock() *Block {
	b := &Block{Index: 0, Timestamp: 1728000000, Txs: []Transaction{}, PrevHash: "0"}
	b.Mine(Difficulty)
	return b
}
