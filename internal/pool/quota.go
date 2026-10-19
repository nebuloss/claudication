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

// quotaNote is what an account's live quota last wrote, and when.
type quotaNote struct {
	at     time.Time
	stored store.AccountQuota
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
// Written at once when it changes whether the account can serve — that is
// what routing reads — and otherwise at most every quotaWriteEvery.
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
	if due {
		p.quotaSeen[id] = quotaNote{at: now, stored: stored}
	}
	p.mu.Unlock()
	if !due {
		return
	}
	// The detail a poll writes is richer than headers carry; keep it when the
	// answer brought none.
	if stored.Detail == "" {
		ctx, cancel := context.WithTimeout(context.Background(), bookkeepingTimeout)
		defer cancel()
		if a, err := p.store.Account(ctx, id); err == nil {
			stored.Detail = a.Quota.Detail
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), bookkeepingTimeout)
	defer cancel()
	if err := p.store.SetAccountQuota(ctx, id, stored); err != nil {
		p.log.Debug("could not record an answer's quota", "account", id, "err", err)
	}
}
