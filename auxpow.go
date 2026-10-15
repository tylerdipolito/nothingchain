package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
)

// AuxPoWMagic is the standard merged mining marker used in Bitcoin coinbases (\xfa\xbe\x6d\x6d).
var AuxPoWMagic = []byte{0xfa, 0xbe, 0x6d, 0x6d}

// DefaultGoAuxBits represents an accessible initial difficulty target for testing
// (~4 leading hex zeroes: 0x0000ffff... in 256-bit space).
// Compact representation: 0x1f00ffff
const DefaultGoAuxBits uint32 = 0x1f00ffff

// BitcoinHeader is the standard 80-byte serialized Bitcoin block header.
type BitcoinHeader struct {
	Version    int32    `json:"version"`
	PrevBlock  string   `json:"prev_block"`  // 32-byte hex (big-endian display)
	MerkleRoot string   `json:"merkle_root"` // 32-byte hex (big-endian display)
	Timestamp  uint32   `json:"timestamp"`
	Bits       uint32   `json:"bits"`
	Nonce      uint32   `json:"nonce"`
}

// Serialize80Bytes serializes the Bitcoin header into its exact 80-byte binary format.
func (h BitcoinHeader) Serialize80Bytes() ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, h.Version); err != nil {
		return nil, err
	}
	prevBytes, err := hex.DecodeString(h.PrevBlock)
	if err != nil || len(prevBytes) != 32 {
		return nil, fmt.Errorf("invalid prev_block hex (must be 32 bytes)")
	}
	// Bitcoin protocol stores hashes in little-endian order on the wire
	buf.Write(reverseBytes(prevBytes))

	merkleBytes, err := hex.DecodeString(h.MerkleRoot)
	if err != nil || len(merkleBytes) != 32 {
		return nil, fmt.Errorf("invalid merkle_root hex (must be 32 bytes)")
	}
	buf.Write(reverseBytes(merkleBytes))

	if err := binary.Write(buf, binary.LittleEndian, h.Timestamp); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.LittleEndian, h.Bits); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.LittleEndian, h.Nonce); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Hash returns the double-SHA256 hash of the 80-byte Bitcoin header in big-endian hex.
func (h BitcoinHeader) Hash() (string, error) {
	raw, err := h.Serialize80Bytes()
	if err != nil {
		return "", err
	}
	sum := DoubleSHA256(raw)
	// Return in standard human-readable big-endian hex (reversed wire bytes)
	return hex.EncodeToString(reverseBytes(sum[:])), nil
}

// AuxPoW contains the auxiliary proof linking a GoChain block to a parent Bitcoin block.
type AuxPoW struct {
	ParentHeader      BitcoinHeader `json:"parent_header"`
	CoinbaseTxHex     string        `json:"coinbase_tx_hex"`
	CoinbaseBranch    []string      `json:"coinbase_branch"` // List of 32-byte hex hashes
	CoinbaseIndex     int           `json:"coinbase_index"`  // Typically 0 for coinbase
	ChainMerkleBranch []string      `json:"chain_merkle_branch,omitempty"`
	ChainIndex        int           `json:"chain_index,omitempty"`
}

// DoubleSHA256 computes SHA256(SHA256(b)).
func DoubleSHA256(b []byte) [32]byte {
	first := sha256.Sum256(b)
	return sha256.Sum256(first[:])
}

func reverseBytes(b []byte) []byte {
	out := make([]byte, len(b))
	for i := 0; i < len(b); i++ {
		out[i] = b[len(b)-1-i]
	}
	return out
}

// BitsToTarget converts a Bitcoin compact bits value to a 256-bit target integer.
func BitsToTarget(bits uint32) *big.Int {
	exponent := bits >> 24
	mantissa := bits & 0x007fffff
	var target big.Int
	if exponent <= 3 {
		target.SetUint64(uint64(mantissa))
		target.Rsh(&target, uint(8*(3-exponent)))
	} else {
		target.SetUint64(uint64(mantissa))
		target.Lsh(&target, uint(8*(exponent-3)))
	}
	return &target
}

// TargetToBits converts a 256-bit target integer back to compact bits.
func TargetToBits(target *big.Int) uint32 {
	if target == nil || target.Sign() <= 0 {
		return 0
	}
	bytes := target.Bytes()
	exponent := uint32(len(bytes))
	var mantissa uint32

	if exponent <= 3 {
		for i := 0; i < len(bytes); i++ {
			mantissa = (mantissa << 8) | uint32(bytes[i])
		}
		mantissa <<= 8 * (3 - exponent)
	} else {
		mantissa = (uint32(bytes[0]) << 16) | (uint32(bytes[1]) << 8) | uint32(bytes[2])
	}

	if mantissa&0x00800000 != 0 {
		mantissa >>= 8
		exponent++
	}
	return (exponent << 24) | (mantissa & 0x007fffff)
}

// CheckMerkleBranch computes the Merkle root from a leaf hash and a branch.
func CheckMerkleBranch(leaf [32]byte, branch [][32]byte, index int) [32]byte {
	curr := leaf
	for _, sibling := range branch {
		if index&1 == 0 {
			// Left child
			combined := append(curr[:], sibling[:]...)
			curr = DoubleSHA256(combined)
		} else {
			// Right child
			combined := append(sibling[:], curr[:]...)
			curr = DoubleSHA256(combined)
		}
		index >>= 1
	}
	return curr
}

// TargetToWork computes the cumulative work (2^256 / (target + 1)).
func TargetToWork(target *big.Int) *big.Int {
	if target == nil || target.Sign() <= 0 {
		return big.NewInt(0)
	}
	two256 := new(big.Int).Lsh(big.NewInt(1), 256)
	denom := new(big.Int).Add(target, big.NewInt(1))
	return new(big.Int).Div(two256, denom)
}

// ValidateAuxPoW verifies the AuxPoW against a candidate GoChain block and its required target.
func ValidateAuxPoW(auxBlock *Block, auxpow *AuxPoW, targetBits uint32) error {
	if auxpow == nil {
		return fmt.Errorf("missing auxpow proof")
	}

	// 1. Verify Parent Header meets GoChain's target difficulty
	parentHashHex, err := auxpow.ParentHeader.Hash()
	if err != nil {
		return fmt.Errorf("compute parent header hash: %w", err)
	}
	parentHashBytes, err := hex.DecodeString(parentHashHex)
	if err != nil {
		return fmt.Errorf("invalid parent hash hex: %w", err)
	}

	parentHashInt := new(big.Int).SetBytes(parentHashBytes)
	goTarget := BitsToTarget(targetBits)
	if parentHashInt.Cmp(goTarget) > 0 {
		return fmt.Errorf("parent block work insufficient for GoChain: hash %s exceeds target %064x", parentHashHex, goTarget)
	}

	// 2. Decode and verify Coinbase Transaction
	cbBytes, err := hex.DecodeString(auxpow.CoinbaseTxHex)
	if err != nil {
		return fmt.Errorf("invalid coinbase tx hex: %w", err)
	}
	cbHash := DoubleSHA256(cbBytes)

	// 3. Verify Coinbase commitment to the GoChain block hash
	expectedAuxHashBytes, err := hex.DecodeString(auxBlock.Hash)
	if err != nil {
		return fmt.Errorf("invalid aux block hash hex: %w", err)
	}

	// Look for AuxPoWMagic (0xfabe6d6d) + 32-byte aux block hash in the coinbase payload
	magicIndex := bytes.Index(cbBytes, AuxPoWMagic)
	if magicIndex == -1 {
		return fmt.Errorf("coinbase tx does not contain AuxPoW magic header (0xfabe6d6d)")
	}

	commitmentOffset := magicIndex + len(AuxPoWMagic)
	if len(cbBytes) < commitmentOffset+32 {
		return fmt.Errorf("coinbase tx truncated after magic header")
	}

	embeddedHash := cbBytes[commitmentOffset : commitmentOffset+32]
	// If multi-chain Merkle tree is used, verify ChainMerkleBranch, otherwise direct comparison
	if len(auxpow.ChainMerkleBranch) > 0 {
		var branch [][32]byte
		for _, bHex := range auxpow.ChainMerkleBranch {
			hBytes, err := hex.DecodeString(bHex)
			if err != nil || len(hBytes) != 32 {
				return fmt.Errorf("invalid chain merkle branch element")
			}
			var b32 [32]byte
			copy(b32[:], hBytes)
			branch = append(branch, b32)
		}
		var leaf32 [32]byte
		copy(leaf32[:], expectedAuxHashBytes)
		calculatedRoot := CheckMerkleBranch(leaf32, branch, auxpow.ChainIndex)
		if !bytes.Equal(embeddedHash, calculatedRoot[:]) {
			return fmt.Errorf("chain merkle branch root does not match coinbase commitment")
		}
	} else {
		if !bytes.Equal(embeddedHash, expectedAuxHashBytes) {
			return fmt.Errorf("coinbase commitment does not match GoChain block hash: embedded %x vs expected %x", embeddedHash, expectedAuxHashBytes)
		}
	}

	// 4. Verify Coinbase Merkle Branch into Parent Bitcoin Header's MerkleRoot
	var cbBranch [][32]byte
	for _, bHex := range auxpow.CoinbaseBranch {
		hBytes, err := hex.DecodeString(bHex)
		if err != nil || len(hBytes) != 32 {
			return fmt.Errorf("invalid coinbase merkle branch element")
		}
		var b32 [32]byte
		copy(b32[:], hBytes)
		cbBranch = append(cbBranch, b32)
	}

	merkleRoot := CheckMerkleBranch(cbHash, cbBranch, auxpow.CoinbaseIndex)
	expectedParentRootBytes, err := hex.DecodeString(auxpow.ParentHeader.MerkleRoot)
	if err != nil || len(expectedParentRootBytes) != 32 {
		return fmt.Errorf("invalid parent merkle root hex")
	}

	// In wire order (little endian), compare against the reverse of big-endian hex display
	if !bytes.Equal(merkleRoot[:], reverseBytes(expectedParentRootBytes)) {
		return fmt.Errorf("coinbase transaction does not link to parent merkle root")
	}

	return nil
}

// RetargetDifficulty adjusts difficulty based on observed block time vs target block time.
// targetTimespan: expected duration for N blocks (e.g., 10 blocks * 60 seconds = 600s).
func RetargetDifficulty(currentBits uint32, actualTimespan int64, targetTimespan int64) uint32 {
	if targetTimespan <= 0 {
		return currentBits
	}

	// Clamp actual timespan between [target/4, target*4] to prevent sudden swings
	if actualTimespan < targetTimespan/4 {
		actualTimespan = targetTimespan / 4
	}
	if actualTimespan > targetTimespan*4 {
		actualTimespan = targetTimespan * 4
	}

	currentTarget := BitsToTarget(currentBits)
	newTarget := new(big.Int).Mul(currentTarget, big.NewInt(actualTimespan))
	newTarget.Div(newTarget, big.NewInt(targetTimespan))

	// Ensure target does not exceed max target (lowest difficulty)
	maxTarget := BitsToTarget(DefaultGoAuxBits)
	if newTarget.Cmp(maxTarget) > 0 {
		newTarget.Set(maxTarget)
	}

	return TargetToBits(newTarget)
}
