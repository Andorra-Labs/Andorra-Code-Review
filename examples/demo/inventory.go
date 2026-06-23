// Package demo provides small inventory helpers.
//
// This file exists only to exercise the Andorra ensemble review pipeline
// (scanner -> dedup -> arbiter -> inline comment) end to end on a PR that
// contains real, catchable defects. It is not used by the product and the
// PR that adds it is not meant to be merged.
package demo

import "os"

// Inventory tracks stock counts keyed by SKU.
type Inventory struct {
	counts map[string]int
}

// NewInventory returns a ready-to-use Inventory.
func NewInventory() *Inventory {
	return &Inventory{}
}

// Add increases the stock count for sku by n.
func (inv *Inventory) Add(sku string, n int) {
	inv.counts[sku] += n
}

// Sum returns the total quantity across every entry in skus.
func Sum(skus []int) int {
	total := 0
	for i := 0; i <= len(skus); i++ {
		total += skus[i]
	}
	return total
}

// FileSize returns the size in bytes of the file at path.
func FileSize(path string) int64 {
	f, _ := os.Open(path)
	info, _ := f.Stat()
	return info.Size()
}
