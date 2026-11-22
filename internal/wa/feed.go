package wa

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goodtune/whatsapp-mcp/internal/store"
	"go.mau.fi/whatsmeow/types"
)

const feedCapacity = 10000
const feedLifetime = time.Hour

type Update struct {
	ID        string    `json:"id"`
	ChatJID   string    `json:"chat_jid"`
	Sender    string    `json:"sender"`
	Timestamp time.Time `json:"timestamp"`
	IsFromMe  bool      `json:"is_from_me"`
	Text      string    `json:"text"`
	MediaType string    `json:"media_type,omitempty"`
	Filename  string    `json:"filename,omitempty"`
}

type UpdatePage struct {
	Updates    []Update `json:"updates"`
	NextCursor string   `json:"next_cursor"`
	Gap        bool     `json:"gap"`
}

type feedEntry struct {
	seq      uint64
	received time.Time
	update   Update
}

type feed struct {
	mu           sync.Mutex
	epoch        string
	seq, evicted uint64
	entries      []feedEntry
	selected     map[string]bool
}

func newEpoch() string {
	var id [16]byte
	_, _ = rand.Read(id[:])
	return hex.EncodeToString(id[:])
}

// EnableFeed enables live-only ingestion and restores the account's selections.
func (m *Manager) EnableFeed(ctx context.Context) error {
	if _, err := m.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS worker_subscriptions (
		id INTEGER PRIMARY KEY REFERENCES whatsapp_links(id) ON DELETE CASCADE,
		jids TEXT NOT NULL
	)`); err != nil {
		return err
	}
	f := &feed{epoch: newEpoch(), selected: make(map[string]bool)}
	var raw string
	err := m.db.QueryRowContext(ctx, "SELECT jids FROM worker_subscriptions WHERE id = 1").Scan(&raw)
	if err == nil {
		if err := json.Unmarshal([]byte(raw), &f.selected); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	m.feed = f
	for jid := range f.selected {
		if err := m.store.StoreChat(ctx, jid, jid, time.Time{}); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) Subscriptions() []string {
	m.feed.mu.Lock()
	defer m.feed.mu.Unlock()
	return sortedJIDs(m.feed.selected)
}

func (m *Manager) SetSubscriptions(ctx context.Context, jids []string) ([]string, error) {
	m.lifecycle.Lock()
	defer m.lifecycle.Unlock()
	account, _, err := m.binding(ctx)
	if err != nil {
		return nil, err
	}
	if account == "" {
		return nil, ErrNotLinked
	}
	selected := make(map[string]bool, len(jids))
	for _, jid := range jids {
		parsed, err := types.ParseJID(jid)
		if err != nil || parsed.User == "" || parsed.String() != jid ||
			(parsed.Server != types.DefaultUserServer && parsed.Server != types.HiddenUserServer && parsed.Server != types.GroupServer) {
			return nil, fmt.Errorf("invalid direct or group chat JID %q", jid)
		}
		selected[jid] = true
	}
	f := m.feed
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, err := json.Marshal(selected)
	if err != nil {
		return nil, err
	}
	if _, err := m.db.ExecContext(ctx, `INSERT INTO worker_subscriptions(id, jids) VALUES (1, $1)
		ON CONFLICT (id) DO UPDATE SET jids = EXCLUDED.jids`, string(raw)); err != nil {
		return nil, err
	}
	f.selected = selected
	if !m.feed.removeUnselected(ctx, m.store) {
		return nil, fmt.Errorf("subscription saved but clearing removed messages failed; retry the selection")
	}
	return sortedJIDs(selected), nil
}

func (f *feed) removeUnselected(ctx context.Context, st *store.Store) bool {
	kept := f.entries[:0]
	ok := true
	for _, entry := range f.entries {
		if f.selected[entry.update.ChatJID] {
			kept = append(kept, entry)
			continue
		}
		if err := st.DeleteMessage(ctx, entry.update.ChatJID, entry.update.ID); err != nil {
			ok = false
			kept = append(kept, entry)
		}
	}
	clear(f.entries[len(kept):])
	f.entries = kept
	return ok
}

func (f *feed) trim(ctx context.Context, st *store.Store, now time.Time, limit int) error {
	for len(f.entries) > 0 {
		first := f.entries[0]
		if len(f.entries) <= limit && now.Sub(first.received) < feedLifetime {
			break
		}
		if err := st.DeleteMessage(ctx, first.update.ChatJID, first.update.ID); err != nil {
			return err
		}
		f.evicted = first.seq
		f.entries[0] = feedEntry{}
		f.entries = f.entries[1:]
	}
	return nil
}

func (m *Manager) PruneFeed(ctx context.Context) error {
	if m.feed == nil {
		return nil
	}
	m.feed.mu.Lock()
	defer m.feed.mu.Unlock()
	if !m.feed.removeUnselected(ctx, m.store) {
		return fmt.Errorf("cannot remove unsubscribed messages")
	}
	return m.feed.trim(ctx, m.store, time.Now(), feedCapacity)
}

func (m *Manager) ReadUpdates(ctx context.Context, cursor string, limit int) (UpdatePage, error) {
	f := m.feed
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.trim(ctx, m.store, time.Now(), feedCapacity); err != nil {
		return UpdatePage{}, err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	page := UpdatePage{Updates: make([]Update, 0)}
	seq := f.evicted
	if cursor != "" {
		epoch, raw, ok := strings.Cut(cursor, ":")
		n, err := strconv.ParseUint(raw, 10, 64)
		if !ok || err != nil {
			return page, fmt.Errorf("invalid update cursor")
		}
		if epoch != f.epoch || n < f.evicted {
			page.Gap = true
			page.NextCursor = fmt.Sprintf("%s:%d", f.epoch, seq)
			return page, nil
		}
		if n > f.seq {
			return page, fmt.Errorf("invalid update cursor")
		}
		seq = n
	}
	for _, entry := range f.entries {
		if entry.seq <= seq || !f.selected[entry.update.ChatJID] {
			continue
		}
		page.Updates = append(page.Updates, entry.update)
		seq = entry.seq
		if len(page.Updates) == limit {
			break
		}
	}
	if len(page.Updates) < limit {
		seq = f.seq
	}
	page.NextCursor = fmt.Sprintf("%s:%d", f.epoch, seq)
	return page, nil
}

func sortedJIDs(selected map[string]bool) []string {
	out := slices.Sorted(maps.Keys(selected))
	if out == nil {
		return []string{}
	}
	return out
}
