package safetylexicon

import "hash/fnv"

// bloomFilter is a self-contained Bloom filter (no third-party dependency) used
// as the §9.3 layer-1 prefilter. It is built once at construction from the
// sorted corpus and is read-only thereafter, so test() is lock-free and safe
// for concurrent use (BR-3.5).
//
// Sizing (BR-4.3): the bit-count m is the next power of two ≥ bitsPerTerm·n, and
// k = numHashes. With bitsPerTerm = 12 and k = 8 the false-positive rate at the
// realized corpus size (~1500 terms) is well under the 1% HARD bound
// (UNIT-022); the power-of-two m lets us index with a mask instead of a modulo.
// The FP bound is a TESTED invariant, not a comment — see UNIT-022.
type bloomFilter struct {
	bits []uint64
	mask uint64 // m-1; m is a power of two so (h & mask) == (h % m)
	k    uint
}

const (
	bloomBitsPerTerm = 12
	bloomNumHashes   = 8
)

// newBloomFilter allocates a filter sized for n terms. n<1 is clamped to 1 so an
// (impossible, floor-guarded) empty corpus still yields a valid filter.
func newBloomFilter(n int) *bloomFilter {
	if n < 1 {
		n = 1
	}
	target := uint64(n) * bloomBitsPerTerm
	m := uint64(64)
	for m < target {
		m <<= 1
	}
	return &bloomFilter{
		bits: make([]uint64, m/64),
		mask: m - 1,
		k:    bloomNumHashes,
	}
}

// hashes derives the two base hashes for Kirsch–Mitzenmacher double-hashing from
// a single FNV-1a/64 digest. h2 is forced non-zero so the probe stride never
// collapses to a single bit.
func (b *bloomFilter) hashes(s string) (h1, h2 uint64) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	sum := h.Sum64()
	h1 = sum & 0xffffffff
	h2 = sum >> 32
	if h2 == 0 {
		h2 = 0x9e3779b97f4a7c15 // golden-ratio fallback stride
	}
	return h1, h2
}

func (b *bloomFilter) add(s string) {
	h1, h2 := b.hashes(s)
	for i := uint(0); i < b.k; i++ {
		idx := (h1 + uint64(i)*h2) & b.mask
		b.bits[idx>>6] |= 1 << (idx & 63)
	}
}

func (b *bloomFilter) test(s string) bool {
	h1, h2 := b.hashes(s)
	for i := uint(0); i < b.k; i++ {
		idx := (h1 + uint64(i)*h2) & b.mask
		if b.bits[idx>>6]&(1<<(idx&63)) == 0 {
			return false
		}
	}
	return true
}
