package capture

// Content-defined chunking, for storing recorded bodies once.
//
// A client resends its whole conversation on every turn, so a recorded chat's
// request bodies are each mostly the one before: the same bytes, a few more at
// the end, and a cache marker moved somewhere in the middle. Cutting each body
// where its content says — not every N bytes — makes the cuts land in the same
// places in the parts two bodies share, so those parts are the same chunks and
// are stored once; only the chunks around a change are new. It knows nothing of
// JSON, of any API or provider: it is the bytes, which is what keeps the store
// generic, and what keeps every body exactly as it passed.
//
// The cut points come from a rolling hash over a byte table (FastCDC, Xia et al.
// 2016, with its normalised chunking: a stricter mask before the average size
// and a looser one after, which narrows the spread of sizes). Chunks are 2 to
// 64 KiB, about 8 KiB on average.
//
// The table and the masks are part of the stored format: changing either moves
// every cut, and nothing stored before would share a chunk with anything after.
// Correctness does not depend on them — a chunk is found by its hash — only the
// saving does.

const (
	chunkMin = 2 << 10
	chunkAvg = 8 << 10
	chunkMax = 64 << 10

	// FastCDC's masks for an 8 KiB average at normalisation level 2: 15 bits
	// set before the average, 11 after, spread across the word.
	maskS uint64 = 0x0000d9f003530000
	maskL uint64 = 0x0000d90003530000
)

// gear is the rolling hash's table: 256 fixed random words, from splitmix64
// over a fixed seed so every build cuts alike.
var gear = func() (g [256]uint64) {
	x := uint64(0x636c617564696361) // "claudica"
	for i := range g {
		x += 0x9e3779b97f4a7c15
		z := x
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		g[i] = z ^ (z >> 31)
	}
	return g
}()

// cut is where the first chunk of b ends.
func cut(b []byte) int {
	n := len(b)
	if n <= chunkMin {
		return n
	}
	if n > chunkMax {
		n = chunkMax
	}
	normal := min(chunkAvg, n)
	var fp uint64
	i := chunkMin
	for ; i < normal; i++ {
		fp = (fp << 1) + gear[b[i]]
		if fp&maskS == 0 {
			return i + 1
		}
	}
	for ; i < n; i++ {
		fp = (fp << 1) + gear[b[i]]
		if fp&maskL == 0 {
			return i + 1
		}
	}
	return n
}

// chunks cuts b where its content says. The chunks alias b.
func chunks(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		n := cut(b)
		out = append(out, b[:n])
		b = b[n:]
	}
	return out
}
