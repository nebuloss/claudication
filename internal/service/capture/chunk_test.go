package capture

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

func randomBytes(n int, seed uint64) []byte {
	r := rand.New(rand.NewPCG(seed, seed))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

// Chunks put back together are the body, each within its bounds.
func TestChunksAreTheBody(t *testing.T) {
	for _, n := range []int{0, 1, chunkMin, chunkMin + 1, 100_000, 1 << 20} {
		b := randomBytes(n, uint64(n))
		cs := chunks(b)
		if got := bytes.Join(cs, nil); !bytes.Equal(got, b) {
			t.Fatalf("%d bytes: the chunks are not the body", n)
		}
		for i, c := range cs {
			last := i == len(cs)-1
			if len(c) > chunkMax || (!last && len(c) < chunkMin) {
				t.Errorf("%d bytes: chunk %d is %d bytes", n, i, len(c))
			}
		}
	}
}

// What a client does on its next turn — the same conversation with a few
// bytes changed in the middle and more at the end — shares nearly every
// chunk with the turn before, which is the whole saving.
func TestTheNextTurnSharesItsChunks(t *testing.T) {
	turn := randomBytes(600_000, 1)
	next := append(append([]byte{}, turn...), randomBytes(20_000, 2)...)
	next[300_000] ^= 0xff // a cache marker moved

	seen := map[string]bool{}
	for _, c := range chunks(turn) {
		seen[string(c)] = true
	}
	var fresh int
	cs := chunks(next)
	for _, c := range cs {
		if !seen[string(c)] {
			fresh += len(c)
		}
	}
	// The change costs about a chunk each side of it, and the new end.
	if fresh > 20_000+4*chunkMax {
		t.Errorf("%d new bytes in the next turn's %d chunks, want about the 20000 added", fresh, len(cs))
	}
	avg := len(next) / len(cs)
	if avg < chunkMin || avg > chunkMax/2 {
		t.Errorf("chunks average %d bytes, want near %d", avg, chunkAvg)
	}
}
