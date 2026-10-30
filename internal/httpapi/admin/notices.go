package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/memlimit"
	"claudication/internal/store"
)

// What the Overview's notices say: the problems the gateway has now, each a
// sentence and a link to where it is dealt with. Worked out from the state as
// it is on every read — nothing is stored, nothing is dismissed: a notice is
// there while its problem is and gone once it is not, and it is read again
// when a topic it is worked out from changes (docs/data-architecture.md).

const (
	noticeError = "error"
	noticeWarn  = "warn"
	noticeInfo  = "info"
)

// notice is one problem, as the UI lists it.
type notice struct {
	// ID is stable while the problem lasts, for the list to key rows on.
	ID       string `json:"id"`
	Severity string `json:"severity"`
	// Kind is what it is about: gateway, account, model, client.
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// Detail is the upstream's or the gateway's own words, when they say
	// more than the title.
	Detail string `json:"detail,omitempty"`
	// Provider names whose it is, for the UI to label: the gateway keeps no
	// display names for providers.
	Provider string `json:"provider,omitempty"`
	// Link is the UI path where it is dealt with.
	Link string `json:"link,omitempty"`
	// Since is when it began, or At when it will end, when either is known.
	Since time.Time `json:"since,omitzero"`
	Until time.Time `json:"until,omitzero"`
}

// The thresholds, each the point past which the operator would want to know
// before a client tells them.
const (
	// A client is failing when its last streakAt requests all failed, or
	// more than half of at least rateMin did, inside clientWindow.
	clientWindow = 15 * time.Minute
	streakAt     = 5
	rateMin      = 10
	// An account's window, or a client's budget, this full is close to
	// refusing.
	nearlyFull = 90.0
	// The process this close to its memory ceiling is close to being killed.
	memoryNear = 0.85
	// How far back a billing refusal is still news.
	refusalWindow = time.Hour
)

// handleNotices is the gateway's current problems, the worst first.
func (s *Admin) handleNotices(w http.ResponseWriter, r *http.Request) {
	out, err := s.notices(r.Context(), time.Now())
	if err != nil {
		s.log.Error("notices", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not work out the notices")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"notices": out})
}

func (s *Admin) notices(ctx context.Context, now time.Time) ([]notice, error) {
	var out []notice
	add := func(n notice) { out = append(out, n) }

	if !s.serving.On() {
		add(notice{ID: "gateway-off", Severity: noticeError, Kind: "gateway",
			Title: "The gateway is switched off: no client is served", Link: "/settings"})
	}
	if !s.modelChecks.On() {
		add(notice{ID: "checks-off", Severity: noticeInfo, Kind: "gateway",
			Title:  "Model checks are off",
			Detail: "A model that stops working on an account is found only when a client's request fails on it.",
			Link:   "/settings/models"})
	}
	if m := memlimit.Read(); m.Limit > 0 && float64(m.RSS) >= memoryNear*float64(m.Limit) {
		add(notice{ID: "memory", Severity: noticeWarn, Kind: "gateway",
			Title: fmt.Sprintf("The gateway is using %.0f%% of its memory limit", 100*float64(m.RSS)/float64(m.Limit)),
			Detail: fmt.Sprintf("%d MiB of %d MiB: past the limit, the system stops it.",
				m.RSS>>20, m.Limit>>20)})
	}

	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.accountNotices(ctx, now, accounts, add); err != nil {
		return nil, err
	}

	for _, m := range s.models.FailingEverywhere(ctx) {
		name := m.Name
		if name == "" {
			name = m.Model
		}
		add(notice{ID: "failing:" + m.Provider + ":" + m.Model, Severity: noticeError, Kind: "model",
			Provider: m.Provider,
			Title:    name + " fails on every account that has it",
			Detail:   "Clients are not offered it until a check or a request finds it working again.",
			Link:     "/accounts"})
	}
	for _, a := range accounts {
		if a.Disabled() {
			continue
		}
		off, err := s.store.AutoOffModels(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		for _, model := range sortedKeys(off) {
			add(notice{ID: "auto-off:" + a.ID + ":" + model, Severity: noticeWarn, Kind: "model",
				Provider: a.Provider,
				Title:    model + " was switched off on " + a.DisplayName() + " after failing for half an hour",
				Detail:   "It is switched back on the first time it works there.",
				Link:     "/accounts/" + url.PathEscape(a.ID) + "/models"})
		}
	}

	if err := s.clientNotices(ctx, now, add); err != nil {
		return nil, err
	}

	if n, last, err := s.store.CodeSeen(ctx, "content_check", now.Add(-refusalWindow)); err != nil {
		return nil, err
	} else if n > 0 {
		add(notice{ID: "content-check", Severity: noticeWarn, Kind: "gateway", Provider: "anthropic",
			Title:  fmt.Sprintf("Anthropic refused %d %s in the last hour as a third-party app", n, plural(n, "request", "requests")),
			Detail: "“Third-party apps now draw from your extra usage”: refused for what the request contained, not the account. docs/refused-requests.md has the triggers found so far.",
			Link:   "/activity/requests?code=content_check",
			Since:  last})
	}

	rank := map[string]int{noticeError: 0, noticeWarn: 1, noticeInfo: 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Severity] < rank[out[j].Severity] })
	return out, nil
}

// accountNotices: an account that must be signed in again, or soon will;
// one sitting out; one nearly out of room; a provider none of whose accounts
// can serve.
func (s *Admin) accountNotices(ctx context.Context, now time.Time, accounts []store.Account, add func(notice)) error {
	status, err := s.poolStatus(ctx)
	if err != nil {
		return err
	}
	connection := func(a store.Account) string { return "/accounts/" + url.PathEscape(a.ID) + "/connection" }
	usage := func(a store.Account) string { return "/accounts/" + url.PathEscape(a.ID) + "/usage" }
	enabled := map[string]bool{}
	for _, a := range accounts {
		if a.Disabled() {
			continue
		}
		enabled[a.Provider] = true
		name := a.DisplayName()
		switch {
		case a.NeedsReauth():
			add(notice{ID: "reauth:" + a.ID, Severity: noticeError, Kind: "account", Provider: a.Provider,
				Title:  name + " must be signed in again",
				Detail: "Its sign-in has ended, and no refresh brings it back: reconnect it to serve from it again.",
				Link:   connection(a)})
			continue
		case a.NeedsReauthSoon():
			d, _ := a.RefreshWindow()
			add(notice{ID: "reauth-soon:" + a.ID, Severity: noticeWarn, Kind: "account", Provider: a.Provider,
				Title:  fmt.Sprintf("%s's sign-in ends in %s", name, roughly(d)),
				Detail: "Refreshing cannot push that date back; reconnect it before then.",
				Link:   connection(a), Until: a.RefreshExpiresAt})
		}
		if until, ok := status.Cooling[a.ID]; ok && until.After(now) {
			add(notice{ID: "cooling:" + a.ID, Severity: noticeWarn, Kind: "account", Provider: a.Provider,
				Title: name + " is sitting out", Detail: a.LastError, Link: usage(a), Until: until})
		}
		q := a.Quota
		for _, win := range []struct {
			label string
			util  float64
			reset time.Time
		}{{"5-hour", q.FiveHourUtil, q.FiveHourReset}, {"weekly", q.SevenDayUtil, q.SevenDayReset}} {
			if win.util < nearlyFull {
				continue
			}
			sev, title := noticeWarn, fmt.Sprintf("%s has used %.0f%% of its %s limit", name, win.util, win.label)
			if win.util >= 100 {
				sev, title = noticeError, fmt.Sprintf("%s is out of its %s limit", name, win.label)
			}
			add(notice{ID: "quota:" + a.ID + ":" + win.label, Severity: sev, Kind: "account", Provider: a.Provider,
				Title: title, Link: usage(a), Until: win.reset})
		}
	}
	for _, p := range s.providers {
		if !enabled[p.ID()] {
			continue
		}
		st, err := s.pool.Status(ctx, p.ID())
		if err != nil {
			return err
		}
		if st.Usable == 0 {
			add(notice{ID: "no-account:" + p.ID(), Severity: noticeError, Kind: "account", Provider: p.ID(),
				Title:  "No account can serve now",
				Detail: "Every account switched on is sitting out, out of room or signed out: requests go to the one back soonest, and are refused if none is.",
				Link:   "/accounts"})
		}
	}
	return nil
}

// clientNotices: a client whose requests keep failing; one near or past its
// day's token budget; a model name of one that serves nothing.
func (s *Admin) clientNotices(ctx context.Context, now time.Time, add func(notice)) error {
	list, err := s.store.Clients(ctx)
	if err != nil {
		return err
	}
	outcomes, err := s.store.ClientOutcomes(ctx, now.Add(-clientWindow))
	if err != nil {
		return err
	}
	for _, c := range list {
		if c.DisabledAt != nil {
			continue
		}
		page := "/clients/" + url.PathEscape(c.ID)
		if o := outcomes[c.ID]; o != nil {
			streak := o.Streak >= streakAt
			rate := o.Requests >= rateMin && 2*o.Failures > o.Requests
			if streak || rate {
				title := fmt.Sprintf("%s's last %d requests failed", c.Name, o.Streak)
				if !streak {
					title = fmt.Sprintf("%s: %d of its %d requests in the last 15 minutes failed", c.Name, o.Failures, o.Requests)
				}
				detail := fmt.Sprintf("Latest: %s", o.Code)
				if o.Status != 0 {
					detail = fmt.Sprintf("Latest: %d %s", o.Status, o.Code)
				}
				if o.Error != "" {
					detail += " — " + o.Error
				}
				add(notice{ID: "client-failing:" + c.ID, Severity: noticeError, Kind: "client",
					Title: title, Detail: detail, Link: page + "/requests?status=failed", Since: o.LastFailed})
			}
		}
		if c.TokenBudget > 0 {
			spent, err := s.budgets.SpentBy(ctx, c.ID)
			if err != nil {
				return err
			}
			if pct := 100 * float64(spent) / float64(c.TokenBudget); pct >= nearlyFull {
				sev, title := noticeWarn, fmt.Sprintf("%s has used %.0f%% of its daily token budget", c.Name, pct)
				if pct >= 100 {
					sev, title = noticeError, c.Name+" is out of its daily token budget: its requests are refused"
				}
				add(notice{ID: "budget:" + c.ID, Severity: sev, Kind: "client", Title: title, Link: page + "/usage"})
			}
		}
		// One notice per client, its names gathered by reason: a client bound
		// to a family no account has is three or four names at once, and a row
		// each buried everything else.
		var (
			dead    []string
			reasons []string
			byWhy   = map[string][]string{}
		)
		for _, sl := range c.Slots {
			_, why := s.models.Resolve(ctx, c.ModelsOff, store.Binding{Model: sl.Model, Follows: sl.Follows})
			if why == "" {
				continue
			}
			if _, seen := byWhy[why]; !seen {
				reasons = append(reasons, why)
			}
			byWhy[why] = append(byWhy[why], strconv.Quote(sl.Name))
			dead = append(dead, sl.Name)
		}
		if len(dead) > 0 {
			title := fmt.Sprintf("%s's model name %q serves nothing", c.Name, dead[0])
			if len(dead) > 1 {
				title = fmt.Sprintf("%s: %d of its model names serve nothing", c.Name, len(dead))
			}
			parts := make([]string, len(reasons))
			for i, why := range reasons {
				parts[i] = strings.Join(byWhy[why], ", ") + ": " + why
				if len(dead) == 1 {
					parts[i] = why
				}
			}
			add(notice{ID: "slots:" + c.ID, Severity: noticeWarn, Kind: "client",
				Title: title, Detail: strings.Join(parts, "; "), Link: page + "/models"})
		}
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// roughly is a duration as a person says it: "2 days", "5 hours".
func roughly(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	case d >= 2*time.Minute:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	return "moments"
}
