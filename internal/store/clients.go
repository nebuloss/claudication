package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"claudication/internal/service/clients"
)

// Clients: what a key is for. See clients and client_slots in schema.sql, and
// internal/service/clients for the types and their slots.
//
// A client's id is its key's id. The store keeps what is true of a client —
// its type, its mode, its slots' names and bindings — and checks
// what has to hold whatever the type: a model name has a model's shape and is
// the client's only once, and a slot is bound to one model or one moving
// target. Which slots a type has is internal/service/clients's to say. A
// client's type is written once, when it is made, and nothing here changes
// it. Which API it speaks is its type's (clients.Type.API), not stored.

// ErrClientNotFound is a client that does not exist, or a key that is no
// client (the gateway's own).
var ErrClientNotFound = errors.New("no such client")

// ErrInvalidClient is a change to a client that cannot be made. Its text says
// why and is meant for the operator.
var ErrInvalidClient = errors.New("invalid client")

type invalidClient string

func (e invalidClient) Error() string        { return string(e) }
func (e invalidClient) Is(target error) bool { return target == ErrInvalidClient }

// Client is one client and the key it holds.
type Client struct {
	APIKey
	Type       string
	Mode       clients.Mode
	DisabledAt *time.Time
	// ConfiguredAt is when the client was first set up with its key; see
	// configured_at in schema.sql.
	ConfiguredAt *time.Time
	Slots        []ClientSlot
}

// ClientSlot is one of a client's model slots.
type ClientSlot struct {
	Slot string
	// Name is the model name the client's configuration asks for.
	Name string
	// Model is the real model it is bound to, or Follows the moving target;
	// exactly one is set.
	Model     string
	Follows   clients.Follow
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Binding is what a slot is bound to: a real model, or a target to follow.
type Binding struct {
	Model   string
	Follows clients.Follow
}

// check is a binding as it is stored, or why it cannot be.
func (b Binding) check() (Binding, error) {
	b.Model = strings.TrimSpace(b.Model)
	switch {
	case b.Model != "" && b.Follows != "":
		return Binding{}, invalidClient("a slot is bound to a model or follows a target, not both")
	case b.Model == "" && b.Follows == "":
		return Binding{}, invalidClient("a slot is bound to a model or follows a target")
	case b.Follows != "" && !b.Follows.Valid():
		return Binding{}, invalidClient("no such target to follow: " + string(b.Follows))
	}
	return b, nil
}

// Spec is the client's type, as internal/service/clients defines it. A type
// this release does not know reads as custom, which every client can be.
func (c Client) Spec() clients.Type {
	if t, ok := clients.Find(c.Type); ok {
		return t
	}
	return clients.Custom
}

// NewSlot is a slot a client is created with.
type NewSlot struct {
	Slot, Name string
	Binding
}

// CreateClient makes a client and the key it holds, in one transaction, and
// returns the key's plaintext once, as CreateKey does.
func (s *Store) CreateClient(ctx context.Context, name, typ string,
	limits KeyLimits, slots []NewSlot) (Client, string, error) {
	t, ok := clients.Find(typ)
	if !ok {
		return Client{}, "", invalidClient("no such client type: " + typ)
	}
	var (
		key       APIKey
		plaintext string
	)
	err := s.inTx(ctx, "create client", func(tx *sql.Tx) error {
		var err error
		if key, plaintext, err = s.insertKey(ctx, tx, name, limits); err != nil {
			return invalidClient(err.Error())
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO clients (key_id, type, mode) VALUES (?, ?, ?)`,
			key.ID, t.ID, string(clients.Binding)); err != nil {
			return fmt.Errorf("create client: %w", err)
		}
		now := time.Now()
		for i, sl := range slots {
			if _, ok := t.SlotFor(sl.Slot); !ok {
				return invalidClient(t.Label + " has no slot " + sl.Slot)
			}
			if err := insertSlot(ctx, tx, key.ID, sl, now.Add(time.Duration(i)*time.Millisecond)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Client{}, "", err
	}
	c, err := s.Client(ctx, key.ID)
	return c, plaintext, err
}

// insertSlot writes one slot, its name and binding checked.
func insertSlot(ctx context.Context, tx *sql.Tx, keyID string, sl NewSlot, at time.Time) error {
	name, err := checkSlotName(sl.Name)
	if err != nil {
		return err
	}
	b, err := sl.Binding.check()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO client_slots (key_id, slot, name, model, follows, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		keyID, sl.Slot, name, b.Model, string(b.Follows), ms(at), ms(at)); err != nil {
		return slotWriteError(err, name)
	}
	return nil
}

// slotWriteError says which of the table's rules a write broke.
func slotWriteError(err error, name string) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "client_slots.key_id, client_slots.name"):
		return invalidClient("this client already has a model named " + name)
	case strings.Contains(msg, "client_slots.key_id, client_slots.slot"):
		return invalidClient("this client already has that slot")
	}
	return fmt.Errorf("write slot: %w", err)
}

// checkSlotName is a model name as it is stored: a model's shape, since the
// configuration names it where it would name a model.
func checkSlotName(name string) (string, error) {
	name = ModelKey(name)
	if !clients.ValidName(name) {
		return "", invalidClient("a model name is lower-case letters, digits, dots and dashes, up to 64")
	}
	return name, nil
}

// clientColumns are a client's own columns, read beside its key's.
const clientColumns = `c.type, c.mode, c.disabled_at, c.configured_at`

// Clients lists every client, oldest first, with its key and its slots. The
// gateway's own key is no client and is not in it.
func (s *Store) Clients(ctx context.Context) ([]Client, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+keyColumns+`, `+clientColumns+`
		   FROM api_keys JOIN clients c ON c.key_id = api_keys.id
		  ORDER BY api_keys.created_at`)
	if err != nil {
		return nil, fmt.Errorf("list clients: %w", err)
	}
	defer rows.Close()
	var out []Client
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, fmt.Errorf("list clients: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list clients: %w", err)
	}
	slots, err := s.slotsBy(ctx, "")
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Slots = ordered(out[i].Spec(), slots[out[i].ID])
	}
	return out, nil
}

// Client reads one client by id.
func (s *Store) Client(ctx context.Context, id string) (Client, error) {
	c, err := scanClient(s.db.QueryRowContext(ctx,
		`SELECT `+keyColumns+`, `+clientColumns+`
		   FROM api_keys JOIN clients c ON c.key_id = api_keys.id
		  WHERE api_keys.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Client{}, ErrClientNotFound
	}
	if err != nil {
		return Client{}, fmt.Errorf("load client: %w", err)
	}
	slots, err := s.slotsBy(ctx, id)
	if err != nil {
		return Client{}, err
	}
	c.Slots = ordered(c.Spec(), slots[id])
	return c, nil
}

func scanClient(sc scanner) (Client, error) {
	var (
		c                    Client
		mode                 string
		disabled, configured sql.NullInt64
	)
	key, err := scanKeyAround(sc, nil, []any{&c.Type, &mode, &disabled, &configured})
	if err != nil {
		return Client{}, err
	}
	c.APIKey, c.Mode = key, clients.Mode(mode)
	c.DisabledAt, c.ConfiguredAt = optionalMS(disabled), optionalMS(configured)
	return c, nil
}

// slotsBy reads the slots of one client, or of every client for "", by
// client id.
func (s *Store) slotsBy(ctx context.Context, keyID string) (map[string][]ClientSlot, error) {
	q := `SELECT key_id, slot, name, model, follows, created_at, updated_at FROM client_slots`
	var args []any
	if keyID != "" {
		q += ` WHERE key_id = ?`
		args = append(args, keyID)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("client slots: %w", err)
	}
	defer rows.Close()
	out := map[string][]ClientSlot{}
	for rows.Next() {
		var (
			id               string
			sl               ClientSlot
			follows          string
			created, updated int64
		)
		if err := rows.Scan(&id, &sl.Slot, &sl.Name, &sl.Model, &follows, &created, &updated); err != nil {
			return nil, fmt.Errorf("client slot: %w", err)
		}
		sl.Follows = clients.Follow(follows)
		sl.CreatedAt, sl.UpdatedAt = fromMS(created), fromMS(updated)
		out[id] = append(out[id], sl)
	}
	return out, rows.Err()
}

// ordered is a client's slots in its type's order, then its own in the order
// they were added.
func ordered(t clients.Type, slots []ClientSlot) []ClientSlot {
	rank := func(id string) int {
		for i, s := range t.Slots {
			if s.ID == id {
				return i
			}
		}
		return len(t.Slots)
	}
	sort.SliceStable(slots, func(i, j int) bool {
		a, b := rank(slots[i].Slot), rank(slots[j].Slot)
		if a != b {
			return a < b
		}
		return slots[i].CreatedAt.Before(slots[j].CreatedAt)
	})
	if slots == nil {
		slots = []ClientSlot{}
	}
	return slots
}

// SetSlot renames a client's slot and binds it. An empty name keeps the one
// it has.
func (s *Store) SetSlot(ctx context.Context, keyID, slot, name string, b Binding) error {
	b, err := b.check()
	if err != nil {
		return err
	}
	return s.inTx(ctx, "set slot", func(tx *sql.Tx) error {
		var current string
		err := tx.QueryRowContext(ctx,
			`SELECT name FROM client_slots WHERE key_id = ? AND slot = ?`, keyID, slot).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrClientNotFound
		}
		if err != nil {
			return fmt.Errorf("set slot: %w", err)
		}
		if strings.TrimSpace(name) == "" {
			name = current
		}
		if name, err = checkSlotName(name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE client_slots SET name = ?, model = ?, follows = ?, updated_at = ? WHERE key_id = ? AND slot = ?`,
			name, b.Model, string(b.Follows), ms(time.Now()), keyID, slot); err != nil {
			return slotWriteError(err, name)
		}
		return nil
	})
}

// AddSlot gives a custom client a slot of its own.
//
// Its id is made up rather than taken from its first name: the name can be
// changed and the id cannot, so an id that was a name would keep that name
// from ever being given to another slot of the client once renamed.
func (s *Store) AddSlot(ctx context.Context, keyID, name string, b Binding) error {
	name, err := checkSlotName(name)
	if err != nil {
		return err
	}
	return s.inTx(ctx, "add slot", func(tx *sql.Tx) error {
		var typ string
		err := tx.QueryRowContext(ctx, `SELECT type FROM clients WHERE key_id = ?`, keyID).Scan(&typ)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrClientNotFound
		}
		if err != nil {
			return fmt.Errorf("add slot: %w", err)
		}
		if t, ok := clients.Find(typ); ok && !t.Custom {
			return invalidClient(t.Label + "'s slots are its configuration's; only a custom client names its own")
		}
		id, err := newSlotID()
		if err != nil {
			return err
		}
		return insertSlot(ctx, tx, keyID, NewSlot{Slot: id, Name: name, Binding: b}, time.Now())
	})
}

// newSlotID is an id for an operator's own slot: "s-" and random hex, which no
// type's slot id has, and a valid custom slot id (clients.ValidName).
func newSlotID() (string, error) {
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("make slot id: %w", err)
	}
	return "s-" + hex.EncodeToString(raw), nil
}

// RemoveSlot takes a custom client's slot away, freeing its name.
func (s *Store) RemoveSlot(ctx context.Context, keyID, slot string) error {
	c, err := s.Client(ctx, keyID)
	if err != nil {
		return err
	}
	if !c.Spec().Custom {
		return invalidClient(c.Spec().Label + "'s slots are its configuration's; only a custom client's can be removed")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM client_slots WHERE key_id = ? AND slot = ?`, keyID, slot)
	if err != nil {
		return fmt.Errorf("remove slot: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrClientNotFound
	}
	return nil
}

// SetClientMode puts a custom client in binding or direct mode. Both stay
// stored: its slots and its switched-off models wait for the mode they
// belong to.
func (s *Store) SetClientMode(ctx context.Context, keyID string, mode clients.Mode) error {
	if !mode.Valid() {
		return invalidClient("no such mode: " + string(mode))
	}
	c, err := s.Client(ctx, keyID)
	if err != nil {
		return err
	}
	if mode == clients.Direct && !c.Spec().Custom {
		return invalidClient(c.Spec().Label + " names its own slots; only a custom client can name real models")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE clients SET mode = ? WHERE key_id = ?`, string(mode), keyID); err != nil {
		return fmt.Errorf("set client mode: %w", err)
	}
	return nil
}

// SetClientFitImages switches a client's image fitting on or off: whether
// a request of its carrying more than 20 images has each capped at 2000 px
// (internal/relay/passes, ShrinkImages). Any type may have it, since any can
// send images; it acts only on a request Claude serves.
func (s *Store) SetClientFitImages(ctx context.Context, keyID string, on bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE clients SET fit_images = ? WHERE key_id = ?`, on, keyID)
	if err != nil {
		return fmt.Errorf("switch image fitting: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrClientNotFound
	}
	return nil
}

// SetClientNameChats switches the gateway's naming of a client's chats on or
// off. Refused for a type that names its own (clients.Type, NamesItsChats):
// reading that name spends nothing, so it has no switch, and a stored flag
// would be one the gateway never reads (internal/httpapi/gateway).
func (s *Store) SetClientNameChats(ctx context.Context, keyID string, on bool) error {
	c, err := s.Client(ctx, keyID)
	if err != nil {
		return err
	}
	if t := c.Spec(); t.NamesItsChats {
		return invalidClient(t.Label + " names its own chats: the gateway only reads the name, so there is no switch")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE clients SET name_chats = ? WHERE key_id = ?`, on, keyID)
	if err != nil {
		return fmt.Errorf("switch chat naming: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrClientNotFound
	}
	return nil
}

// MarkConfigured records that a client has been set up with its key, the
// first time only: its install command was fetched, or it made a request.
func (s *Store) MarkConfigured(ctx context.Context, keyID string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE clients SET configured_at = ? WHERE key_id = ? AND configured_at IS NULL`,
		ms(time.Now()), keyID); err != nil {
		return fmt.Errorf("mark client configured: %w", err)
	}
	return nil
}

// SetClientDisabled switches a client off, or back on. Off, its key is
// refused before anything is routed; its type, slots and history stay.
func (s *Store) SetClientDisabled(ctx context.Context, keyID string, disabled bool) error {
	q := `UPDATE clients SET disabled_at = NULL WHERE key_id = ?`
	args := []any{keyID}
	if disabled {
		q = `UPDATE clients SET disabled_at = COALESCE(disabled_at, ?) WHERE key_id = ?`
		args = []any{ms(time.Now()), keyID}
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("switch client: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrClientNotFound
	}
	return nil
}

// ClientSlotNamed is the slot of one client's that a request's model names,
// if any: one read by the client's (key_id, name) index, on the path of every
// request a binding-mode client makes.
func (s *Store) ClientSlotNamed(ctx context.Context, keyID, name string) (ClientSlot, bool, error) {
	var (
		sl      ClientSlot
		follows string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT slot, name, model, follows FROM client_slots WHERE key_id = ? AND name = ?`,
		keyID, ModelKey(name)).Scan(&sl.Slot, &sl.Name, &sl.Model, &follows)
	if errors.Is(err, sql.ErrNoRows) {
		return ClientSlot{}, false, nil
	}
	if err != nil {
		return ClientSlot{}, false, fmt.Errorf("look up model name: %w", err)
	}
	sl.Follows = clients.Follow(follows)
	return sl, true, nil
}

// ClientSlots are one client's slots, for its model list and its refusals. A
// key that is no client has none.
func (s *Store) ClientSlots(ctx context.Context, keyID string) ([]ClientSlot, error) {
	if keyID == "" {
		return nil, nil
	}
	c, err := s.Client(ctx, keyID)
	if errors.Is(err, ErrClientNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c.Slots, nil
}
