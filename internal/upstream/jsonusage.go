package upstream

import "encoding/json"

// maxJSONUsageBytes bounds the copy kept for parsing. A message envelope is a
// few KB; anything past this is not a shape we can read usage out of anyway,
// and a relay must not grow its memory with the response it is passing on.
const maxJSONUsageBytes = 1 << 20

// jsonUsage recovers the usage figures from a non-streaming response.
//
// Streaming responses give theirs up event by event to the SSE scanner. A
// non-streaming one carries the same numbers in its JSON body, and without
// this every such request was recorded as zero tokens — which reads as "cost
// nothing" when it means "not counted", and quietly understated every total on
// the Usage tab.
//
// This does not make Lane A a body-parsing proxy. It reads a copy of bytes
// that have already gone to the client, exactly as the SSE scanner does, and
// nothing it decides can change what was relayed.
type jsonUsage struct {
	usage *Usage
	buf   []byte
}

func newJSONUsage(usage *Usage) *jsonUsage {
	return &jsonUsage{usage: usage}
}

func (j *jsonUsage) feed(chunk []byte) {
	if len(j.buf) >= maxJSONUsageBytes {
		return
	}
	if room := maxJSONUsageBytes - len(j.buf); len(chunk) > room {
		chunk = chunk[:room]
	}
	j.buf = append(j.buf, chunk...)
}

// done parses what was collected. A body that is truncated, not JSON, or an
// error envelope simply yields nothing: usage is a statistic, and failing to
// find it is not a failure of the request.
func (j *jsonUsage) done() {
	if len(j.buf) == 0 {
		return
	}
	var probe struct {
		Usage struct {
			InputTokens              *int `json:"input_tokens"`
			OutputTokens             *int `json:"output_tokens"`
			CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(j.buf, &probe); err != nil {
		return
	}
	if v := probe.Usage.InputTokens; v != nil {
		j.usage.InputTokens = *v
	}
	if v := probe.Usage.OutputTokens; v != nil {
		j.usage.OutputTokens = *v
	}
	if v := probe.Usage.CacheReadInputTokens; v != nil {
		j.usage.CacheReadTokens = *v
	}
	if v := probe.Usage.CacheCreationInputTokens; v != nil {
		j.usage.CacheCreationTokens = *v
	}
}
