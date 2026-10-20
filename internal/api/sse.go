package api

import (
	"bytes"
	"strings"
)

// Reading a server-sent event stream that arrives in pieces, for the
// translating dialects: each takes its upstream's stream as the relay writes
// it, a chunk at a time, and acts on whole frames.
//
// The provider meters read the same streams with a line scanner of their own
// (internal/provider/*/ssescan.go), and are left that way: they watch for
// one field, must not hold a megabyte-long line in memory to find it, and
// cut such a line as it passes, which a frame reader that hands a whole
// frame to its caller cannot do.

// NextFrame removes the next whole frame from buf: the bytes before the
// first blank line, in either line ending. ok is false while no frame is
// whole yet.
func NextFrame(buf *bytes.Buffer) (frame []byte, ok bool) {
	b := buf.Bytes()
	lf := bytes.Index(b, []byte("\n\n"))
	crlf := bytes.Index(b, []byte("\r\n\r\n"))
	end, sep := lf, 2
	if crlf >= 0 && (end < 0 || crlf < end) {
		end, sep = crlf, 4
	}
	if end < 0 {
		return nil, false
	}
	frame = append([]byte(nil), b[:end]...)
	buf.Next(end + sep)
	return frame, true
}

// FrameFields reads a frame's event name and data. Several data lines join
// with a newline, as the format says: a reader that kept only the last would
// cut the JSON short. A comment line (": ping") and any other field are
// skipped; a frame of nothing else has no event and no data.
func FrameFields(frame []byte) (event string, data []byte) {
	var lines []string
	for _, line := range strings.Split(string(frame), "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			lines = append(lines, strings.TrimPrefix(line[len("data:"):], " "))
		}
	}
	if lines != nil {
		data = []byte(strings.Join(lines, "\n"))
	}
	return event, data
}
