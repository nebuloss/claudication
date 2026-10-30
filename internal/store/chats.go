package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"claudication/internal/live"
	"claudication/internal/service/clients"
)

// A chat is a conversation as the client that made it named it.
//
// Rolled up from usage_events rather than kept as its own table. A chat has no
// life of its own here — it is a grouping of requests, it is created by the
// first request that names it and it ends when they stop — so a table would
// only be a cache of this query that could disagree with it. Retention comes
// free for the same reason: when the pruner drops old events the chats they
// belonged to stop existing, with nothing left behind pointing at nothing.

// InternalPath marks a request the gateway made on its own behalf rather than
// relayed for a client — today, the call that asks a model to name a chat.
//
// RecordUsage files such a row as kind 2, which is the discriminator the
// rollup uses to answer "whose chat is this", because those rows are part of
// the conversation's cost but say nothing about who was having it. A path
// rather than a key name: names are editable and get reused, and this has to
// keep working when someone renames something.
const InternalPath = "/internal/title"

// Chat is one conversation's totals.
type Chat struct {
	// ID is the conversation id the client sent. Never empty in this list:
	// requests that named no conversation are counted separately, because a
	// single row lumping unrelated traffic together is not a chat.
	ID string `json:"id"`
	// Title is what the gateway asked a model to call this conversation, or
	// empty when it never did — titling is off, or the chat predates it, or the
	// request was refused. Empty is common and the UI falls back to the client
	// and the id, which is why nothing here depends on it.
	Title string `json:"title"`
	// TitleManual is set when an operator typed the title, which holds it
	// against automatic renames.
	TitleManual bool   `json:"title_manual"`
	Client      string `json:"client"`
	// Surface is how its client was run, as clients.SurfaceTitle names it —
	// Interactive, Headless (claude -p), Agent SDK — or empty for a client
	// whose User-Agent does not say. The latest relayed request's, like the
	// client. What lets an operator tell a headless Claude Code chat, which
	// is never named, from an interactive one not named yet.
	Surface string `json:"surface,omitempty"`
	// KeyName and AccountEmail are whichever served it most recently. A chat
	// normally keeps one of each for its whole life; when a key is rotated or
	// an account rotates under it mid-conversation, the latest is the one an
	// operator is looking for.
	KeyName      string `json:"key_name"`
	AccountEmail string `json:"account_email"`
	// Models used, in first-seen order. A chat that switched model mid-way is
	// ordinary — a plan step on Opus, the edits on Sonnet — so this is a list
	// rather than a single value.
	Models   []string `json:"models"`
	Requests int64    `json:"requests"`
	// Turns are the requests that were the conversation itself: Requests
	// less the ones that named it, the client's own and the gateway's, which
	// are its cost but not its turns.
	Turns        int64     `json:"turns"`
	Errors       int64     `json:"errors"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CacheTokens  int64     `json:"cache_tokens"`
	First        time.Time `json:"first"`
	Last         time.Time `json:"last"`
	// Context is where the conversation's context stands: what its client is
	// resending each turn, and how often it has compacted. See context.go.
	Context ChatContext `json:"context"`
}

// Tokens is the billable total, the same four columns UsageEvent.Tokens adds.
func (c Chat) Tokens() int64 {
	return c.InputTokens + c.OutputTokens + c.CacheTokens
}

// ChatReport is the chat list plus what fell outside it.
type ChatReport struct {
	Chats []Chat `json:"chats"`
	// Unattributed is every request whose client named no conversation, as one
	// bucket. It is reported rather than hidden: it is the only way an operator
	// finds out that something is talking to the gateway anonymously, and a UI
	// that silently dropped those requests would show a token total that did
	// not add up to the one on the Overview.
	Unattributed Chat `json:"unattributed"`
	// Total is how many distinct chats exist in the window, which is not
	// len(Chats) once the limit bites.
	Total int `json:"total"`
}

// Chats rolls usage up per conversation, busiest first.
//
// Ordered by tokens rather than by recency because the question this answers
// is "what did that cost" — the expensive chat is the one worth finding, and
// it is not usually the most recent. The caller can sort by any column once it
// has the page.
func (s *Store) Chats(ctx context.Context, since time.Time, limit int) (ChatReport, error) {
	return s.ChatsIn(ctx, since, limit, UsageScope{})
}

// MaxChatsPage is the most chats ChatsIn lists at once; see MaxRequestsPage.
const MaxChatsPage = 500

// ChatsIn is Chats over the conversations one account served or one key
// paid for, at least once in the window.
//
// A chat in scope is shown whole, not just the requests that matched: a chat
// the pool moved between accounts part way is still one chat, and its totals
// are what it cost. Where it went is on the chat itself.
func (s *Store) ChatsIn(ctx context.Context, since time.Time, limit int, scope UsageScope) (ChatReport, error) {
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, MaxChatsPage)
	from := ms(since)

	// Empty for the whole gateway, so that query is exactly what it was.
	inScope, scopeArgs := "", []any{}
	if narrow, narrowArgs := scope.where(); narrow != "" {
		inScope = ` AND conversation_id IN
		    (SELECT conversation_id FROM usage_events
		      WHERE ` + sqlCounted + ` AND at >= ? AND conversation_id <> ''` + narrow + `)`
		scopeArgs = append([]any{from}, narrowArgs...)
	}

	var report ChatReport

	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT conversation_id) FROM usage_events
		  WHERE `+sqlCounted+` AND at >= ? AND conversation_id <> ''`+inScope,
		append([]any{from}, scopeArgs...)...).Scan(&report.Total); err != nil {
		return ChatReport{}, fmt.Errorf("count chats: %w", err)
	}

	// group_concat over an ordered subquery, so the model list is in the order
	// the chat actually used them rather than whatever order the group happened
	// to be scanned in. DISTINCT and ORDER BY cannot be combined in SQLite's
	// group_concat, hence the inner SELECT.
	//
	// The client, key and account are the latest relayed row's (kind = 0): the
	// gateway's own titling rows are part of the cost, not of who was talking.
	rows, err := s.db.QueryContext(ctx,
		`SELECT conversation_id,
		        COALESCE((SELECT client FROM usage_events e1
		                   WHERE e1.conversation_id = e.conversation_id AND e1.kind = 0 AND e1.at >= ?
		                     AND e1.client <> ''
		                   ORDER BY e1.at DESC LIMIT 1), ''),
		        COALESCE((SELECT surface FROM usage_events e5
		                   WHERE e5.conversation_id = e.conversation_id AND e5.kind = 0 AND e5.at >= ?
		                     AND e5.surface <> ''
		                   ORDER BY e5.at DESC LIMIT 1), ''),
		        COUNT(*), COALESCE(SUM(kind = 0), 0),
		        COALESCE(SUM(`+sqlIsError+`), 0),
		        COALESCE(SUM(input_tokens), 0),
		        COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(`+sqlCacheTokens+`), 0),
		        MIN(at), MAX(at),
		        (SELECT group_concat(m) FROM
		            (SELECT DISTINCT model AS m FROM usage_events e2
		              WHERE e2.conversation_id = e.conversation_id AND e2.kind IN (0, 2) AND e2.at >= ?
		                AND e2.model <> ''
		              ORDER BY e2.at)),
		        `+nameOf(nameKey, `(SELECT e3.key_id FROM usage_events e3
		          WHERE e3.conversation_id = e.conversation_id AND e3.kind = 0 AND e3.at >= ?
		          ORDER BY e3.at DESC LIMIT 1)`)+`,
		        `+nameOf(nameAccount, `(SELECT e4.account_id FROM usage_events e4
		          WHERE e4.conversation_id = e.conversation_id AND e4.kind = 0 AND e4.at >= ?
		          ORDER BY e4.at DESC LIMIT 1)`)+`,
		        (SELECT title FROM chat_titles t
		          WHERE t.conversation_id = e.conversation_id),
		        (SELECT manual FROM chat_titles t
		          WHERE t.conversation_id = e.conversation_id)
		   FROM usage_events e
		  WHERE `+sqlCounted+` AND at >= ? AND conversation_id <> ''`+inScope+`
		  GROUP BY conversation_id
		  ORDER BY SUM(`+sqlTokens+`) DESC
		  LIMIT ?`,
		append(append([]any{from, from, from, from, from, from}, scopeArgs...), limit)...)
	if err != nil {
		return ChatReport{}, fmt.Errorf("chats: %w", err)
	}
	defer rows.Close()

	report.Chats = []Chat{}
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return ChatReport{}, fmt.Errorf("chats: %w", err)
		}
		report.Chats = append(report.Chats, c)
	}
	if err := rows.Err(); err != nil {
		return ChatReport{}, fmt.Errorf("chats: %w", err)
	}
	rows.Close()

	// For the listed chats only: the page, not the window, is what is shown.
	ids := make([]string, len(report.Chats))
	for i, c := range report.Chats {
		ids[i] = c.ID
	}
	contexts, err := s.ChatContexts(ctx, ids, since)
	if err != nil {
		return ChatReport{}, err
	}
	for i := range report.Chats {
		report.Chats[i].Context = contexts[report.Chats[i].ID]
	}

	un, err := s.unattributed(ctx, from, scope)
	if err != nil {
		return ChatReport{}, err
	}
	report.Unattributed = un
	return report, nil
}

func scanChat(r scanner) (Chat, error) {
	var (
		c                   Chat
		first, last         *int64
		models              *string
		key, account, title *string
		manual              *int64
	)
	var surface string
	if err := r.Scan(&c.ID, &c.Client, &surface, &c.Requests, &c.Turns, &c.Errors,
		&c.InputTokens, &c.OutputTokens, &c.CacheTokens,
		&first, &last, &models, &key, &account, &title, &manual); err != nil {
		return Chat{}, err
	}
	c.Surface = clients.SurfaceTitle(surface)
	if title != nil {
		c.Title = *title
	}
	c.TitleManual = manual != nil && *manual != 0
	if first != nil {
		c.First = fromMS(*first)
	}
	if last != nil {
		c.Last = fromMS(*last)
	}
	c.Models = []string{}
	if models != nil && *models != "" {
		c.Models = strings.Split(*models, ",")
	}
	if key != nil {
		c.KeyName = *key
	}
	if account != nil {
		c.AccountEmail = *account
	}
	return c, nil
}

// unattributed totals every request that named no conversation.
//
// One bucket, deliberately: these requests have nothing in common except the
// absence, so grouping them further would invent a structure the data does not
// have. Requests is what matters here — it is the number that tells an
// operator how much traffic is arriving unlabelled.
func (s *Store) unattributed(ctx context.Context, from int64, scope UsageScope) (Chat, error) {
	narrow, narrowArgs := scope.where()
	args := append(append(append([]any{from}, narrowArgs...), from), narrowArgs...)
	row := s.db.QueryRowContext(ctx,
		// No client, and not because it is hard to pick one: these rows have
		// nothing in common but the absence of a conversation id, so naming any
		// one of their clients would read as a fact about all of them.
		`SELECT '', '', '',
		        COUNT(*), COALESCE(SUM(kind = 0), 0),
		        COALESCE(SUM(`+sqlIsError+`), 0),
		        COALESCE(SUM(input_tokens), 0),
		        COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(`+sqlCacheTokens+`), 0),
		        MIN(at), MAX(at),
		        (SELECT group_concat(m) FROM
		            (SELECT DISTINCT model AS m FROM usage_events
		              WHERE `+sqlCounted+` AND conversation_id = '' AND at >= ? AND model <> ''`+narrow+`)),
		        '', '', '', 0
		   FROM usage_events WHERE `+sqlCounted+` AND at >= ? AND conversation_id = ''`+narrow,
		args...)

	c, err := scanChat(row)
	if err != nil {
		return Chat{}, fmt.Errorf("unattributed usage: %w", err)
	}
	return c, nil
}

// ChatEvents returns one conversation's latest requests, oldest first.
//
// Oldest first because a chat is read forwards: the interesting shape is how
// the context grew turn by turn, and that is a curve you read left to right.
// The latest, because a chat that outgrows the limit is a long-running one, and
// where it stands now is the question; its first few hundred turns were
// returned here before, which for a session of thousands of requests stopped
// days short of the present.
func (s *Store) ChatEvents(ctx context.Context, id string, limit int) ([]UsageEvent, error) {
	if id == "" {
		return nil, fmt.Errorf("chat events: no conversation id")
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT * FROM (
		    SELECT `+usageEventColumns+`
		      FROM usage_events
		     WHERE conversation_id = ?
		     ORDER BY at DESC, id DESC LIMIT ?)
		  ORDER BY at ASC, id ASC`, id, limit)
	if err != nil {
		return nil, fmt.Errorf("chat events: %w", err)
	}
	defer rows.Close()

	out := []UsageEvent{}
	for rows.Next() {
		e, err := scanUsageEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("chat events: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("chat events: %w", err)
	}
	return out, nil
}

// maxTitle bounds what is stored. The model is asked for something short; this
// is the guard for when it answers with an essay anyway, which a model told to
// be brief does often enough to matter.
const maxTitle = 120

// ChatTitle returns the stored title for a conversation, if it has one.
func (s *Store) ChatTitle(ctx context.Context, id string) (string, bool, error) {
	var title string
	err := s.db.QueryRowContext(ctx,
		`SELECT title FROM chat_titles WHERE conversation_id = ?`, id).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("chat title: %w", err)
	}
	return title, true, nil
}

// ChatTitles are the stored titles of the chats named, by id: what a page of
// the request log shows its chat ids as on hover. One query for the page,
// not one per row; a chat with no title is not in the map.
func (s *Store) ChatTitles(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	seen := map[string]bool{}
	var args []any
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			args = append(args, id)
		}
	}
	if len(args) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT conversation_id, title FROM chat_titles WHERE conversation_id IN (?`+
			strings.Repeat(",?", len(args)-1)+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("chat titles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, fmt.Errorf("chat title: %w", err)
		}
		out[id] = title
	}
	return out, rows.Err()
}

// TitleRecord is a stored title and what it was written from.
type TitleRecord struct {
	Title string
	// Requests and Turns are the conversation's request count and the number
	// of messages the user had typed when the gateway wrote this title. Zero
	// when it did not write it: see chat_titles in schema.sql.
	Requests int
	Turns    int
	// Manual is set when an operator typed the title.
	Manual bool
}

// TitleRecord returns a conversation's title with what it was written from.
func (s *Store) TitleRecord(ctx context.Context, id string) (TitleRecord, bool, error) {
	var r TitleRecord
	err := s.db.QueryRowContext(ctx,
		`SELECT title, requests, turns, manual FROM chat_titles WHERE conversation_id = ?`, id).
		Scan(&r.Title, &r.Requests, &r.Turns, &r.Manual)
	if errors.Is(err, sql.ErrNoRows) {
		return TitleRecord{}, false, nil
	}
	if err != nil {
		return TitleRecord{}, false, fmt.Errorf("chat title: %w", err)
	}
	return r, true, nil
}

// ConversationRequests counts the requests recorded against a conversation,
// from idx_usage_conversation without touching the table.
func (s *Store) ConversationRequests(ctx context.Context, id string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usage_events WHERE conversation_id = ?`, id).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("conversation requests: %w", err)
	}
	return n, nil
}

// clipTitle trims a title and bounds it at maxTitle.
func clipTitle(title string) string {
	title = strings.TrimSpace(title)
	if len(title) > maxTitle {
		// Back to the start of a character: a cut through the middle of one
		// stored invalid UTF-8, which the admin UI then renders as garbage.
		cut := maxTitle
		for cut > 0 && !utf8.RuneStart(title[cut]) {
			cut--
		}
		title = strings.TrimSpace(title[:cut])
	}
	return title
}

// RetitleChat writes a title the gateway generated, replacing any there was,
// and records what it was written from so the next decision to replace it has
// something to compare against.
func (s *Store) RetitleChat(ctx context.Context, id, title, model string, basis TitleRecord) error {
	defer s.changed(live.Chats)
	title = clipTitle(title)
	if id == "" || title == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO chat_titles (conversation_id, title, model, created_at, requests, turns)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (conversation_id) DO UPDATE SET
		   title = excluded.title, model = excluded.model,
		   created_at = excluded.created_at,
		   requests = excluded.requests, turns = excluded.turns, manual = 0`,
		id, title, model, ms(time.Now()), basis.Requests, basis.Turns)
	if err != nil {
		return fmt.Errorf("retitle chat: %w", err)
	}
	return nil
}

// ChatOfKey reports whether a conversation is a key's own: every request
// filed under it, relayed or the client's own naming, is that key's. What
// lets a client's agent name a chat of its own client and no other's
// (internal/httpapi/agent).
//
// Every rather than any: a conversation id is whatever the client sends, so a
// client can put another's id on a request of its own, and "it has one of my
// requests" would then hand it the other's chat. A chat with requests from
// two keys is nobody's to name from here; an operator still can. Refusals
// are no part of it either way: a refused request proves nothing.
func (s *Store) ChatOfKey(ctx context.Context, id, keyID string) (bool, error) {
	if id == "" || keyID == "" {
		return false, nil
	}
	var keys int
	var first string
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT key_id), COALESCE(MIN(key_id), '') FROM usage_events
		  WHERE conversation_id = ? AND `+sqlCounted, id).Scan(&keys, &first)
	if err != nil {
		return false, fmt.Errorf("chat of key: %w", err)
	}
	return keys == 1 && first == keyID, nil
}

// SetManualTitle stores a title an operator typed, replacing any there was.
// The titler never renames a manual title, so it stays until the operator
// types another. No model wrote it, so none is recorded.
func (s *Store) SetManualTitle(ctx context.Context, id, title string) (string, error) {
	defer s.changed(live.Chats)
	title = clipTitle(title)
	if id == "" || title == "" {
		return "", errors.New("a title cannot be empty")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO chat_titles (conversation_id, title, model, created_at, manual)
		 VALUES (?, ?, '', ?, 1)
		 ON CONFLICT (conversation_id) DO UPDATE SET
		   title = excluded.title, model = '',
		   created_at = excluded.created_at, manual = 1`,
		id, title, ms(time.Now()))
	if err != nil {
		return "", fmt.Errorf("set manual title: %w", err)
	}
	return title, nil
}

// SetChatTitle records a title, once.
//
// First writer wins. DO NOTHING rather than REPLACE makes the racing-turns case
// harmless: two turns of a new chat can both decide it needs a title, and the
// loser simply does nothing. Replacing a title on purpose, once a chat has
// outgrown it, is RetitleChat.
func (s *Store) SetChatTitle(ctx context.Context, id, title, model string) error {
	defer s.changed(live.Chats)
	title = clipTitle(title)
	if id == "" || title == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO chat_titles (conversation_id, title, model, created_at)
		 VALUES (?, ?, ?, ?) ON CONFLICT (conversation_id) DO NOTHING`,
		id, title, model, ms(time.Now()))
	if err != nil {
		return fmt.Errorf("set chat title: %w", err)
	}
	return nil
}

// SetClientTitle records the title a client gave its own chat, read off the
// answer to its own naming request. It is the name the client shows, so it
// wins over one the gateway wrote (requests > 0: see chat_titles), which a
// race can have put there first — crush names a session alongside its first
// turn, and the gateway's titler may answer sooner. An operator's title, and
// a client title already read, stay.
func (s *Store) SetClientTitle(ctx context.Context, id, title, model string) error {
	defer s.changed(live.Chats)
	title = clipTitle(title)
	if id == "" || title == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO chat_titles (conversation_id, title, model, created_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT (conversation_id) DO UPDATE SET
		   title = excluded.title, model = excluded.model, created_at = excluded.created_at,
		   requests = 0, turns = 0
		 WHERE chat_titles.manual = 0 AND chat_titles.requests > 0`,
		id, title, model, ms(time.Now()))
	if err != nil {
		return fmt.Errorf("set client chat title: %w", err)
	}
	return nil
}

// PruneChatTitles drops titles whose conversations have no events left.
//
// usage_events is pruned on a schedule, and a title table that was not pruned
// with it would grow without bound while keeping names for conversations
// nobody can look at any more.
func (s *Store) PruneChatTitles(ctx context.Context) (int64, error) {
	defer s.changed(live.Chats)
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM chat_titles WHERE conversation_id NOT IN
		   (SELECT DISTINCT conversation_id FROM usage_events WHERE conversation_id <> '')`)
	if err != nil {
		return 0, fmt.Errorf("prune chat titles: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
