package anthropic

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// Keeping reasoning for a caller that does not carry it.
//
// A caller that asked for thinking is answered with thinking blocks that carry
// the reasoning, and sends them back (reasoning.go). One that did not ask is
// not sent them: Anthropic never answers with a thinking block unasked, and a
// client that did not ask has no reason to keep or return one. But a GPT
// model reasons whether asked or not, and without its reasoning on the next
// turn of a tool loop the model starts each step cold — the backend keeps
// nothing (store:false), so the encrypted item in the answer is the only copy.
//
// So the gateway keeps it, in memory, for the turn the caller sends next,
// keyed by what the reasoning led to: the call it made (its call_id, which
// the caller sends back with the result) or the text it wrote. When an
// assistant turn comes back with that call or text and no thinking block in
// front of it, the items go back in front of it. That works for every
// client, whatever it does with thinking blocks, and for a caller that does
// send them back nothing is added: its own come first.
//
// Bounded, oldest first out, and lost on a restart, which costs the model its
// earlier reasoning and nothing else: a turn without it is still answered.
// The pool keeps a conversation on the account that served it
// (internal/pool), which is the account that can read the items.

// maxReasoningBytes bounds the store. An item's encrypted content is a few
// kilobytes (testdata/chatgpt-reasoning.sse: 1.4 KB); this is thousands of
// turns.
const maxReasoningBytes = 32 << 20

type reasoningStore struct {
	mu    sync.Mutex
	max   int
	bytes int
	order *list.List // of *storedReasoning, oldest at the front
	byKey map[string]*list.Element
}

type storedReasoning struct {
	key   string
	items []carriedReasoning
	size  int
}

func newReasoningStore(maxBytes int) *reasoningStore {
	return &reasoningStore{max: maxBytes, order: list.New(), byKey: map[string]*list.Element{}}
}

// callKey and textKey name what a reasoning led to.
func callKey(callID string) string { return "call:" + callID }

func textKey(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "text:" + hex.EncodeToString(sum[:16])
}

func storeKey(conversation, key string) string { return conversation + "\x00" + key }

// put keeps items as what led to key in a conversation. A nil store keeps
// nothing.
func (s *reasoningStore) put(conversation, key string, items []carriedReasoning) {
	if s == nil || len(items) == 0 {
		return
	}
	k := storeKey(conversation, key)
	size := len(k)
	for _, r := range items {
		size += len(r.ID) + len(r.Encrypted) + len(r.Summary)
	}
	if size > s.max {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.byKey[k]; ok {
		s.remove(e)
	}
	s.byKey[k] = s.order.PushBack(&storedReasoning{key: k, items: items, size: size})
	s.bytes += size
	for s.bytes > s.max {
		s.remove(s.order.Front())
	}
}

func (s *reasoningStore) remove(e *list.Element) {
	r := s.order.Remove(e).(*storedReasoning)
	delete(s.byKey, r.key)
	s.bytes -= r.size
}

// get is what led to key, or nothing. It is kept: a client that resends the
// same history — a retry, the next turn — needs it again.
func (s *reasoningStore) get(conversation, key string) []carriedReasoning {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byKey[storeKey(conversation, key)]
	if !ok {
		return nil
	}
	s.order.MoveToBack(e)
	return e.Value.(*storedReasoning).items
}
