package pool

import (
	"context"
	"time"

	"claudication/internal/provider"
	"claudication/internal/store"
)

// StoredQuota is a provider's report as the store keeps it: the windows mapped
// onto the quota's columns by their length (store.QuotaFromWindows). One
// conversion for the two places a report comes from — the usage poll and a
// relayed answer's headers — so they cannot map a window differently.
func StoredQuota(q provider.Quota) store.AccountQuota {
	windows := make([]store.QuotaWindow, len(q.Windows))
	for i, w := range q.Windows {
		windows[i] = store.QuotaWindow(w)
	}
	at := q.At
	if at.IsZero() {
		at = time.Now()
	}
	return store.QuotaFromWindows(at, windows, q.Limited, q.Detail)
}

// quotaNote is what an account's live quota last wrote, and when, and the
// write in flight for it.
type quotaNote struct {
	at     time.Time
	stored store.AccountQuota
	// writing is that a write for this account is in flight, and next the
	// newest figure noted since, which it writes when it is done.
	writing bool
	next    *store.AccountQuota
}

// quotaWriteEvery bounds how often a relayed answer's quota is written for one
// account while it reads the same. The figures move by a percent at a time;
// a busy account answering several times a second would otherwise write its
// quota row as often, for a screen that refreshes every few seconds at best.
const quotaWriteEvery = 15 * time.Second

// NoteQuota records the quota a relayed answer reported for the account that
// served it. ChatGPT's backend sends it on every response, current to that
// request, which is fresher than any poll; the poll still runs, and is what an
// account that has served nothing is known by.
//
// Kept when it changes whether the account can serve — that is what routing
// reads — and otherwise at most every quotaWriteEvery.
//
// The write is not the caller's: NoteQuota is called by the relay between an
// answer's headers and its body, and a database write there is time to first
// token. So it returns at once and the write runs on its own, one at a time
// per account — a figure noted while one is in flight replaces any waiting
// and is written next, so the row ends at the newest and never goes back.
//
// A report of no windows is nothing to record — the store would read it as
// every window unknown — and is dropped.
func (p *Pool) NoteQuota(id string, q provider.Quota) {
	if len(q.Windows) == 0 {
		return
	}
	stored := StoredQuota(q)
	p.mu.Lock()
	last, seen := p.quotaSeen[id]
	now := p.now()
	due := !seen || now.Sub(last.at) >= quotaWriteEvery ||
		outOfRoom(store.Account{Quota: last.stored}) != outOfRoom(store.Account{Quota: stored})
	if !due {
		p.mu.Unlock()
		return
	}
	last.at, last.stored = now, stored
	if last.writing {
		last.next = &stored
		p.quotaSeen[id] = last
		p.mu.Unlock()
		return
	}
	last.writing = true
	p.quotaSeen[id] = last
	p.quotaWrites.Add(1)
	p.mu.Unlock()
	go p.writeQuota(id, stored)
}

// writeQuota writes an account's noted quota, then whatever was noted while it
// did, until nothing is waiting.
func (p *Pool) writeQuota(id string, q store.AccountQuota) {
	defer p.quotaWrites.Done()
	for {
		p.storeQuota(id, q)
		p.mu.Lock()
		n := p.quotaSeen[id]
		if n.next == nil {
			n.writing = false
			p.quotaSeen[id] = n
			p.mu.Unlock()
			return
		}
		q, n.next = *n.next, nil
		p.quotaSeen[id] = n
		p.mu.Unlock()
	}
}

// storeQuota is one write, with its own deadline as all the pool's
// bookkeeping has.
func (p *Pool) storeQuota(id string, q store.AccountQuota) {
	ctx, cancel := context.WithTimeout(context.Background(), bookkeepingTimeout)
	defer cancel()
	// The detail a poll writes is richer than headers carry; keep it when the
	// answer brought none.
	if q.Detail == "" {
		if a, err := p.store.Account(ctx, id); err == nil {
			q.Detail = a.Quota.Detail
		}
	}
	if err := p.store.SetAccountQuota(ctx, id, q); err != nil {
		p.log.Debug("could not record an answer's quota", "account", id, "err", err)
	}
}
