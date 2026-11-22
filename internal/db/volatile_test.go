package db_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/goodtune/whatsapp-mcp/internal/db"
	"github.com/goodtune/whatsapp-mcp/internal/store"
)

func TestVolatileHistory(t *testing.T) {
	ctx := context.Background()
	disk, dialect, err := db.Open("sqlite:" + filepath.Join(t.TempDir(), "session.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	if err := db.Migrate(ctx, disk, dialect); err != nil {
		t.Fatal(err)
	}

	memory, err := db.OpenVolatileHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	history := store.New(memory)
	if err := history.StoreChat(ctx, "123@s.whatsapp.net", "Test", time.Now()); err != nil {
		t.Fatal(err)
	}
	if chat, err := history.GetChat(ctx, "123@s.whatsapp.net", false); err != nil || chat == nil {
		t.Fatalf("in-memory chat = %v, %v", chat, err)
	}
	if chat, err := store.New(disk).GetChat(ctx, "123@s.whatsapp.net", false); err != nil || chat != nil {
		t.Fatalf("disk chat = %v, %v", chat, err)
	}

	fresh, err := db.OpenVolatileHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if chat, err := store.New(fresh).GetChat(ctx, "123@s.whatsapp.net", false); err != nil || chat != nil {
		t.Fatalf("fresh in-memory chat = %v, %v", chat, err)
	}
}
