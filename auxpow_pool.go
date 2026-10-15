package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"
)

// AuxBlock represents a GoChain block with an attached AuxPoW proof and dynamic target bits.
type AuxBlock struct {
	*Block
	Bits   uint32  `json:"bits"`
	AuxPoW *AuxPoW `json:"auxpow,omitempty"`
}

// ComputeWork returns the 256-bit work contributed by this block.
func (ab *AuxBlock) ComputeWork() *big.Int {
	return TargetToWork(BitsToTarget(ab.Bits))
}

// MiningPoolSimulator simulates a Bitcoin mining pool that constructs merged-mining jobs
// and solves proofs for both Bitcoin and auxiliary chains (like GoChain).
type MiningPoolSimulator struct {
	PoolName      string
	PoolRewardBtc int64
	MinerAddress  string // GoChain reward address
	BtcTipHash    string
	BtcTargetBits uint32
}

func NewMiningPoolSimulator(name string, minerAddress string) *MiningPoolSimulator {
	// Standard simulated Bitcoin target: e.g. 0x1d00ffff (higher difficulty than GoChain)
	return &MiningPoolSimulator{
		PoolName:      name,
		PoolRewardBtc: 312500000, // 3.125 BTC in satoshis
		MinerAddress:  minerAddress,
		BtcTipHash:    "00000000000000000002476d059b02a2818de7c9ff7a36cb926cb129de47a8ae",
		BtcTargetBits: 0x1800ffff, // High Bitcoin difficulty
	}
}

// SolveGoChainJob takes a candidate GoChain block and solves an AuxPoW proof using parent Bitcoin hashing.
func (p *MiningPoolSimulator) SolveGoChainJob(candidate *Block, goTargetBits uint32) (*AuxBlock, error) {
	candidate.Hash = candidate.computeHash()
	targetInt := BitsToTarget(goTargetBits)

	// 1. Build Bitcoin Coinbase Transaction embedding the GoChain BlockHash
	// Format: arbitrary coinbase bytes + AuxPoWMagic (0xfabe6d6d) + 32-byte aux block hash + extra nonce
	cbPrefix := []byte(fmt.Sprintf("\x01\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xff\xff\xff\xff\x2a\x03\x00\x00\x00/Pool:%s/", p.PoolName))
	auxHashBytes, err := hex.DecodeString(candidate.Hash)
	if err != nil {
		return nil, fmt.Errorf("invalid candidate hash: %w", err)
	}

	cbBytes := append(cbPrefix, AuxPoWMagic...)
	cbBytes = append(cbBytes, auxHashBytes...)
	cbSuffix := []byte("\x00\x00\x00\x00\x01\x00\xf2\x05\x2a\x01\x00\x00\x00\x19\x76\xa9\x14pool_btc_script_hash\x88\xac\x00\x00\x00\x00")
	cbBytes = append(cbBytes, cbSuffix...)

	// 2. Build Coinbase Merkle Branch (simulating 3 sibling transactions in Bitcoin block)
	tx1Hash := DoubleSHA256([]byte("btc_tx_1_transfer_alice_bob"))
	tx2Hash := DoubleSHA256([]byte("btc_tx_2_transfer_carol_dave"))
	tx3Hash := DoubleSHA256([]byte("btc_tx_3_transfer_exchange_cold"))

	// Merkle Tree:
	// Level 1: [CB, Tx1] -> H01 = Hash(CB || Tx1), [Tx2, Tx3] -> H23 = Hash(Tx2 || Tx3)
	// Level 2: [H01, H23] -> Root = Hash(H01 || H23)
	cbHash := DoubleSHA256(cbBytes)
	h01 := DoubleSHA256(append(cbHash[:], tx1Hash[:]...))
	h23 := DoubleSHA256(append(tx2Hash[:], tx3Hash[:]...))
	root := DoubleSHA256(append(h01[:], h23[:]...))

	merkleRootHex := hex.EncodeToString(reverseBytes(root[:]))
	coinbaseBranchHex := []string{
		hex.EncodeToString(tx1Hash[:]), // Sibling of CB at level 1
		hex.EncodeToString(h23[:]),     // Sibling of H01 at level 2
	}

	// 3. Construct 80-byte Bitcoin Header
	parentHeader := BitcoinHeader{
		Version:    0x20000000,
		PrevBlock:  p.BtcTipHash,
		MerkleRoot: merkleRootHex,
		Timestamp:  uint32(time.Now().Unix()),
		Bits:       p.BtcTargetBits,
		Nonce:      0,
	}

	// 4. ASIC Hashing Loop (iterating nonce to meet GoChain's target)
	baseTime := uint32(time.Now().Unix())
	for extra := uint32(0); extra < 20; extra++ {
		parentHeader.Timestamp = baseTime + extra
		for nonce := uint32(0); nonce < 2_000_000; nonce++ {
			parentHeader.Nonce = nonce
			headerBytes, err := parentHeader.Serialize80Bytes()
			if err != nil {
				return nil, err
			}
			hashSum := DoubleSHA256(headerBytes)
			hashInt := new(big.Int).SetBytes(reverseBytes(hashSum[:]))

			if hashInt.Cmp(targetInt) <= 0 {
				// Found valid AuxPoW proof for GoChain!
				auxpow := &AuxPoW{
					ParentHeader:   parentHeader,
					CoinbaseTxHex:  hex.EncodeToString(cbBytes),
					CoinbaseBranch: coinbaseBranchHex,
					CoinbaseIndex:  0,
				}

				return &AuxBlock{
					Block:  candidate,
					Bits:   goTargetBits,
					AuxPoW: auxpow,
				}, nil
			}
		}
	}

	return nil, fmt.Errorf("hash iterations exhausted without meeting target")
}

// SolveMultiChainJob simulates merged mining where ONE Bitcoin block commits to MULTIPLE auxiliary chains
// (e.g. GoChain + GameChain) via an auxiliary Merkle tree.
func (p *MiningPoolSimulator) SolveMultiChainJob(candidate *Block, siblingAuxHashes []string, goTargetBits uint32) (*AuxBlock, error) {
	candidate.Hash = candidate.computeHash()
	targetInt := BitsToTarget(goTargetBits)

	auxHashBytes, err := hex.DecodeString(candidate.Hash)
	if err != nil {
		return nil, fmt.Errorf("invalid candidate hash: %w", err)
	}

	// Construct Auxiliary Merkle Tree:
	// GoChain is index 0. Sibling chain (e.g. GameChain) is index 1.
	var auxLeaf [32]byte
	copy(auxLeaf[:], auxHashBytes)

	var chainBranchHex []string
	currRoot := auxLeaf
	for _, sibHex := range siblingAuxHashes {
		sibBytes, err := hex.DecodeString(sibHex)
		if err != nil || len(sibBytes) != 32 {
			return nil, fmt.Errorf("invalid sibling aux hash")
		}
		var sib32 [32]byte
		copy(sib32[:], sibBytes)
		chainBranchHex = append(chainBranchHex, hex.EncodeToString(sibBytes))
		// Merkle pair: [currRoot, sib]
		currRoot = DoubleSHA256(append(currRoot[:], sib32[:]...))
	}

	// Embed the Aux Merkle Root in the Bitcoin Coinbase
	cbPrefix := []byte(fmt.Sprintf("\x01\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xff\xff\xff\xff\x2a\x03\x00\x00\x00/MultiPool:%s/", p.PoolName))
	cbBytes := append(cbPrefix, AuxPoWMagic...)
	cbBytes = append(cbBytes, currRoot[:]...)
	cbSuffix := []byte("\x00\x00\x00\x00\x01\x00\xf2\x05\x2a\x01\x00\x00\x00\x19\x76\xa9\x14pool_btc_script_hash\x88\xac\x00\x00\x00\x00")
	cbBytes = append(cbBytes, cbSuffix...)

	// Single transaction block for simplicity
	cbHash := DoubleSHA256(cbBytes)
	merkleRootHex := hex.EncodeToString(reverseBytes(cbHash[:]))

	parentHeader := BitcoinHeader{
		Version:    0x20000000,
		PrevBlock:  p.BtcTipHash,
		MerkleRoot: merkleRootHex,
		Timestamp:  uint32(time.Now().Unix()),
		Bits:       p.BtcTargetBits,
		Nonce:      0,
	}

	baseTime := uint32(time.Now().Unix())
	for extra := uint32(0); extra < 20; extra++ {
		parentHeader.Timestamp = baseTime + extra
		for nonce := uint32(0); nonce < 2_000_000; nonce++ {
			parentHeader.Nonce = nonce
			headerBytes, err := parentHeader.Serialize80Bytes()
			if err != nil {
				return nil, err
			}
			hashSum := DoubleSHA256(headerBytes)
			hashInt := new(big.Int).SetBytes(reverseBytes(hashSum[:]))

			if hashInt.Cmp(targetInt) <= 0 {
				auxpow := &AuxPoW{
					ParentHeader:      parentHeader,
					CoinbaseTxHex:     hex.EncodeToString(cbBytes),
					CoinbaseBranch:    []string{}, // Coinbase is only tx
					CoinbaseIndex:     0,
					ChainMerkleBranch: chainBranchHex,
					ChainIndex:        0, // GoChain is index 0
				}

				return &AuxBlock{
					Block:  candidate,
					Bits:   goTargetBits,
					AuxPoW: auxpow,
				}, nil
			}
		}
	}

	return nil, fmt.Errorf("hash iterations exhausted")
}

// RandomHash generates a random 32-byte hex string for test simulation.
func RandomHash() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}
