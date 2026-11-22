package wa

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/goodtune/whatsapp-mcp/internal/db"
	"github.com/goodtune/whatsapp-mcp/internal/store"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestFeedSelectionAndRestart(t *testing.T) {
	ctx := context.Background()
	session, dialect, err := db.Open("sqlite:" + filepath.Join(t.TempDir(), "session.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := db.Migrate(ctx, session, dialect); err != nil {
		t.Fatal(err)
	}
	chosen := "123@s.whatsapp.net"
	other := "124@s.whatsapp.net"
	if _, err := session.ExecContext(ctx, "INSERT INTO whatsapp_links(id, account_jid) VALUES (1, $1)", chosen); err != nil {
		t.Fatal(err)
	}
	memory, err := db.OpenVolatileHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	m, err := NewManager(ctx, session, dialect, store.New(memory), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.EnableFeed(ctx); err != nil {
		t.Fatal(err)
	}
	for _, jid := range []string{chosen, other} {
		if err := m.store.StoreChat(ctx, jid, "Test chat", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.SetSubscriptions(ctx, []string{chosen}); err != nil {
		t.Fatal(err)
	}
	for _, jid := range []string{other, chosen} {
		chat, err := types.ParseJID(jid)
		if err != nil {
			t.Fatal(err)
		}
		m.handleMessage(ctx, nil, &events.Message{
			Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "msg", Timestamp: time.Now()},
			Message: &waE2E.Message{Conversation: proto.String("test")},
		})
	}
	page, err := m.ReadUpdates(ctx, "", 1)
	if err != nil || len(page.Updates) != 1 || page.Updates[0].ChatJID != chosen {
		t.Fatalf("page = %+v, %v", page, err)
	}
	empty, err := m.ReadUpdates(ctx, page.NextCursor, 1)
	if err != nil || len(empty.Updates) != 0 || empty.Gap {
		t.Fatalf("next page = %+v, %v", empty, err)
	}
	if persisted, err := store.New(session).ListMessages(ctx, store.ListMessagesFilter{}); err != nil || len(persisted) != 0 {
		t.Fatalf("disk history = %+v, %v", persisted, err)
	}
	newMemory, err := db.OpenVolatileHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer newMemory.Close()
	restarted, err := NewManager(ctx, session, dialect, store.New(newMemory), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.EnableFeed(ctx); err != nil {
		t.Fatal(err)
	}
	if selected := restarted.Subscriptions(); len(selected) != 1 || selected[0] != chosen {
		t.Fatalf("restored = %v", selected)
	}
	gap, err := restarted.ReadUpdates(ctx, page.NextCursor, 1)
	if err != nil || !gap.Gap || len(gap.Updates) != 0 {
		t.Fatalf("restart page = %+v, %v", gap, err)
	}
	m.feed.entries[0].received = time.Now().Add(-feedLifetime)
	expired, err := m.ReadUpdates(ctx, m.feed.epoch+":0", 1)
	if err != nil || !expired.Gap || len(expired.Updates) != 0 {
		t.Fatalf("expired page = %+v, %v", expired, err)
	}
	chat, err := types.ParseJID(chosen)
	if err != nil {
		t.Fatal(err)
	}
	m.handleMessage(ctx, nil, &events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "msg2", Timestamp: time.Now()},
		Message: &waE2E.Message{Conversation: proto.String("test")},
	})
	if _, err := m.SetSubscriptions(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if remaining, err := m.store.ListMessages(ctx, store.ListMessagesFilter{}); err != nil || len(remaining) != 0 {
		t.Fatalf("remaining = %+v, %v", remaining, err)
	}
}
