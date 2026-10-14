package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const UnitsPerGO int64 = 100_000_000
const InitialReward int64 = 10 * UnitsPerGO
const TargetBlockSeconds int64 = 60
const MaxFutureSeconds int64 = 120

func leapYear(year int) bool { return year%4 == 0 && (year%100 != 0 || year%400 == 0) }

// Rewards follow the UTC block timestamp. No floating point enters consensus.
func rewardAt(timestamp int64) int64 {
	halvings := uint(0)
	for year := 2028; halvings < 64; year += 4 {
		if !leapYear(year) {
			continue
		}
		cutoff := time.Date(year, time.February, 29, 0, 0, 0, 0, time.UTC).Unix()
		if timestamp < cutoff {
			break
		}
		halvings++
	}
	if halvings >= 64 {
		return 0
	}
	return InitialReward >> halvings
}
func nextHalving(timestamp int64) string {
	for year := 2028; year <= 9996; year += 4 {
		if !leapYear(year) {
			continue
		}
		cutoff := time.Date(year, time.February, 29, 0, 0, 0, 0, time.UTC)
		if cutoff.Unix() > timestamp {
			return cutoff.Format(time.RFC3339)
		}
	}
	return ""
}
func formatAmount(atoms int64) string {
	whole := atoms / UnitsPerGO
	fraction := atoms % UnitsPerGO
	if fraction == 0 {
		return strconv.FormatInt(whole, 10)
	}
	return fmt.Sprintf("%d.%s", whole, strings.TrimRight(fmt.Sprintf("%08d", fraction), "0"))
}
func parseAmount(input string) (int64, error) {
	parts := strings.Split(input, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, fmt.Errorf("amount must be a nonnegative decimal GO amount")
	}
	for _, part := range parts {
		if part == "" {
			return 0, fmt.Errorf("empty decimal component")
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return 0, fmt.Errorf("amount must use decimal digits only")
			}
		}
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole > math.MaxInt64/UnitsPerGO {
		return 0, fmt.Errorf("amount too large")
	}
	var fraction int64
	if len(parts) == 2 {
		if len(parts[1]) > 8 {
			return 0, fmt.Errorf("GO supports at most 8 decimal places")
		}
		fraction, err = strconv.ParseInt(parts[1]+strings.Repeat("0", 8-len(parts[1])), 10, 64)
		if err != nil {
			return 0, err
		}
	}
	atoms := whole * UnitsPerGO
	if fraction > math.MaxInt64-atoms {
		return 0, fmt.Errorf("amount too large")
	}
	return atoms + fraction, nil
}
