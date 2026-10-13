package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
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
// It is the discriminator the rollup uses to answer "whose chat is this",
// because those rows are part of the conversation's cost but say nothing about
// who was having it. A path rather than a key name: names are editable and get
// reused, and this has to keep working when someone renames something.
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
	// KeyName and AccountEmail are whichever served it most recently. A chat
	// normally keeps one of each for its whole life; when a key is rotated or
	// an account rotates under it mid-conversation, the latest is the one an
	// operator is looking for.
	KeyName      string `json:"key_name"`
	AccountEmail string `json:"account_email"`
	// Models used, in first-seen order. A chat that switched model mid-way is
	// ordinary — a plan step on Opus, the edits on Sonnet — so this is a list
	// rather than a single value.
	Models       []string  `json:"models"`
	Requests     int64     `json:"requests"`
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
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	from := stamp(since)

	var report ChatReport

	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT conversation_id) FROM usage_events
		  WHERE rejected = 0 AND at >= ? AND conversation_id <> ''`, from).Scan(&report.Total); err != nil {
		return ChatReport{}, fmt.Errorf("count chats: %w", err)
	}

	// group_concat over an ordered subquery, so the model list is in the order
	// the chat actually used them rather than whatever order the group happened
	// to be scanned in. DISTINCT and ORDER BY cannot be combined in SQLite's
	// group_concat, hence the inner SELECT.
	rows, err := s.db.QueryContext(ctx,
		`SELECT conversation_id,
		        COALESCE((SELECT client FROM usage_events e1
		                   WHERE e1.conversation_id = e.conversation_id AND e1.rejected = 0 AND e1.at >= ?
		                     AND e1.client <> '' AND e1.path <> ?
		                   ORDER BY e1.at DESC LIMIT 1), ''),
		        COUNT(*),
		        COALESCE(SUM(status >= 400 OR error <> ''), 0),
		        COALESCE(SUM(input_tokens), 0),
		        COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_read_tokens + cache_write_tokens), 0),
		        MIN(at), MAX(at),
		        (SELECT group_concat(m) FROM
		            (SELECT DISTINCT model AS m FROM usage_events e2
		              WHERE e2.conversation_id = e.conversation_id AND e2.rejected = 0 AND e2.at >= ?
		                AND e2.model <> ''
		              ORDER BY e2.at)),
		        (SELECT key_name FROM usage_events e3
		          WHERE e3.conversation_id = e.conversation_id AND e3.rejected = 0 AND e3.at >= ?
		            AND e3.path <> ?
		          ORDER BY e3.at DESC LIMIT 1),
		        (SELECT account_email FROM usage_events e4
		          WHERE e4.conversation_id = e.conversation_id AND e4.rejected = 0 AND e4.at >= ?
		            AND e4.path <> ?
		          ORDER BY e4.at DESC LIMIT 1),
		        (SELECT title FROM chat_titles t
		          WHERE t.conversation_id = e.conversation_id),
		        (SELECT manual FROM chat_titles t
		          WHERE t.conversation_id = e.conversation_id)
		   FROM usage_events e
		  WHERE rejected = 0 AND at >= ? AND conversation_id <> ''
		  GROUP BY conversation_id
		  ORDER BY SUM(input_tokens + output_tokens
		               + cache_read_tokens + cache_write_tokens) DESC
		  LIMIT ?`,
		from, InternalPath, from, from, InternalPath, from, InternalPath, from, limit)
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

	un, err := s.unattributed(ctx, from)
	if err != nil {
		return ChatReport{}, err
	}
	report.Unattributed = un
	return report, nil
}

// rowScanner is what both queries below hand to scanChat.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanChat(r rowScanner) (Chat, error) {
	var (
		c                   Chat
		first, last         string
		models              *string
		key, account, title *string
		manual              *int64
	)
	if err := r.Scan(&c.ID, &c.Client, &c.Requests, &c.Errors,
		&c.InputTokens, &c.OutputTokens, &c.CacheTokens,
		&first, &last, &models, &key, &account, &title, &manual); err != nil {
		return Chat{}, err
	}
	if title != nil {
		c.Title = *title
	}
	c.TitleManual = manual != nil && *manual != 0
	c.First, _ = time.Parse(time.RFC3339Nano, first)
	c.Last, _ = time.Parse(time.RFC3339Nano, last)
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
func (s *Store) unattributed(ctx context.Context, from string) (Chat, error) {
	row := s.db.QueryRowContext(ctx,
		// No client, and not because it is hard to pick one: these rows have
		// nothing in common but the absence of a conversation id, so naming any
		// one of their clients would read as a fact about all of them.
		`SELECT '', '',
		        COUNT(*),
		        COALESCE(SUM(status >= 400 OR error <> ''), 0),
		        COALESCE(SUM(input_tokens), 0),
		        COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_read_tokens + cache_write_tokens), 0),
		        COALESCE(MIN(at), ''), COALESCE(MAX(at), ''),
		        (SELECT group_concat(m) FROM
		            (SELECT DISTINCT model AS m FROM usage_events
		              WHERE rejected = 0 AND conversation_id = '' AND at >= ? AND model <> '')),
		        '', '', '', 0
		   FROM usage_events WHERE rejected = 0 AND at >= ? AND conversation_id = ''`,
		from, from)

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
		    SELECT id, at, key_id, key_name, account_id, account_email, model, path,
		           conversation_id, client, status, streaming,
		           input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
		           duration_ms, first_token_ms, error
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
		var e UsageEvent
		var at string
		var ms, firstMS int64
		if err := rows.Scan(&e.ID, &at, &e.KeyID, &e.KeyName, &e.AccountID, &e.AccountEmail,
			&e.Model, &e.Path, &e.ConversationID, &e.Client, &e.Status, &e.Streaming,
			&e.InputTokens, &e.OutputTokens, &e.CacheReadTokens, &e.CacheWriteTokens,
			&ms, &firstMS, &e.Error); err != nil {
			return nil, fmt.Errorf("chat events: %w", err)
		}
		e.At, _ = time.Parse(time.RFC3339Nano, at)
		e.Duration = time.Duration(ms) * time.Millisecond
		e.FirstToken = time.Duration(firstMS) * time.Millisecond
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

// TitleRecord is a stored title and what it was written from.
type TitleRecord struct {
	Title string
	// Requests and Turns are the conversation's request count and the number
	// of messages the user had typed when the gateway wrote this title. Zero
	// when it did not write it: see migration 023.
	Requests int
	Turns    int
	// Manual is set when an operator typed the title: see migration 024.
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
// from idx_usage_conversation_at without touching the table.
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
		id, title, model, stamp(time.Now()), basis.Requests, basis.Turns)
	if err != nil {
		return fmt.Errorf("retitle chat: %w", err)
	}
	return nil
}

// SetTitleManual switches a titled chat between automatic and manual naming
// without touching its title. found is false when the chat has no title yet:
// manual naming means a name the operator chose, so there has to be one.
func (s *Store) SetTitleManual(ctx context.Context, id string, manual bool) (found bool, err error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE chat_titles SET manual = ? WHERE conversation_id = ?`, manual, id)
	if err != nil {
		return false, fmt.Errorf("set title mode: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetManualTitle stores a title an operator typed, replacing any there was,
// and holds it against automatic renames until RetitleChat writes over it —
// which only an operator's own refresh leads to. No model wrote it, so none
// is recorded.
func (s *Store) SetManualTitle(ctx context.Context, id, title string) (string, error) {
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
		id, title, stamp(time.Now()))
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
	title = clipTitle(title)
	if id == "" || title == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO chat_titles (conversation_id, title, model, created_at)
		 VALUES (?, ?, ?, ?) ON CONFLICT (conversation_id) DO NOTHING`,
		id, title, model, stamp(time.Now()))
	if err != nil {
		return fmt.Errorf("set chat title: %w", err)
	}
	return nil
}

// PruneChatTitles drops titles whose conversations have no events left.
//
// usage_events is pruned on a schedule, and a title table that was not pruned
// with it would grow without bound while keeping names for conversations
// nobody can look at any more.
func (s *Store) PruneChatTitles(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM chat_titles WHERE conversation_id NOT IN
		   (SELECT DISTINCT conversation_id FROM usage_events WHERE conversation_id <> '')`)
	if err != nil {
		return 0, fmt.Errorf("prune chat titles: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
