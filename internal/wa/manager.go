package wa

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"

	appdb "github.com/goodtune/whatsapp-mcp/internal/db"
	"github.com/goodtune/whatsapp-mcp/internal/store"
)

var ErrNotLinked = errors.New("no WhatsApp number linked yet; call link_whatsapp first")

// LinkState is a snapshot of a user's device-linking progress, returned by
// the link_whatsapp MCP tool so the caller can render a QR code or relay a
// pairing code.
type LinkState struct {
	Linked   bool
	JID      string
	QRCode   string // raw QR payload; render this as a QR code, or show qr_png_base64
	QRExpiry time.Time
	PairCode string // phone-number linking code, if a phone number was supplied
	Error    error
}

type pendingLink struct {
	mu        sync.Mutex
	client    *whatsmeow.Client
	qrCode    string
	qrExpiry  time.Time
	pairCode  string
	err       error
	pairDone  chan struct{} // guarded by Manager.mu
	cancelled bool
}

func (p *pendingLink) snapshot() LinkState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return LinkState{QRCode: p.qrCode, QRExpiry: p.qrExpiry, PairCode: p.pairCode, Error: p.err}
}

func (p *pendingLink) setQR(code string, expiry time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.qrCode, p.qrExpiry = code, expiry
}

func (p *pendingLink) setPairCode(code string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pairCode = code
}

func (p *pendingLink) setErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

type Manager struct {
	ctx       context.Context
	db        *sql.DB
	container *sqlstore.Container
	store     *store.Store
	mediaDir  string
	logger    waLog.Logger
	feed      *feed

	lifecycle   sync.Mutex
	eventWrites sync.WaitGroup
	mu          sync.RWMutex
	client      *whatsmeow.Client
	pending     *pendingLink
}

func NewManager(ctx context.Context, db *sql.DB, dialect appdb.Dialect, st *store.Store, mediaDir string) (*Manager, error) {
	logger := waLog.Stdout("WhatsApp", "INFO", true)
	container := sqlstore.NewWithDB(db, dialect.DriverName(), logger)
	if err := container.Upgrade(ctx); err != nil {
		return nil, fmt.Errorf("failed to prepare whatsmeow schema: %w", err)
	}

	return &Manager{
		ctx:       ctx,
		db:        db,
		container: container,
		store:     st,
		mediaDir:  mediaDir,
		logger:    logger,
	}, nil
}

// binding returns the retained account and, when linked, its device.
func (m *Manager) binding(ctx context.Context) (string, string, error) {
	var account string
	var jid sql.NullString
	err := m.db.QueryRowContext(ctx, "SELECT account_jid, jid FROM whatsapp_links WHERE id = 1").Scan(&account, &jid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return account, jid.String, err
}

func (m *Manager) Reconnect(ctx context.Context) error {
	m.lifecycle.Lock()
	defer m.lifecycle.Unlock()
	_, err := m.getClientLocked(ctx)
	if errors.Is(err, ErrNotLinked) {
		return nil
	}
	return err
}

type Status struct {
	Linked    bool   `json:"linked"`
	JID       string `json:"jid,omitempty"`
	Connected bool   `json:"connected"`
}

func (m *Manager) Status(ctx context.Context) (*Status, error) {
	_, jid, err := m.binding(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()
	return &Status{Linked: jid != "", JID: jid, Connected: jid != "" && client != nil && client.IsConnected()}, nil
}

// Logout retires the client before removing any local account data.
func (m *Manager) Logout(ctx context.Context, keepHistory bool) (bool, error) {
	m.lifecycle.Lock()
	defer m.lifecycle.Unlock()

	m.mu.Lock()
	client, pending := m.client, m.pending
	m.client, m.pending = nil, nil
	m.mu.Unlock()
	m.eventWrites.Wait()

	if pending != nil {
		pending.client.Disconnect()
		if pending.pairDone != nil {
			select {
			case <-pending.pairDone:
			case <-ctx.Done():
				m.mu.Lock()
				pending.cancelled = true
				m.pending = pending
				m.mu.Unlock()
				return false, fmt.Errorf("wait for cancelled pairing: %w", ctx.Err())
			}
		}
	}
	remoteUnconfirmed := client != nil && !client.IsLoggedIn()
	if client != nil {
		if client.IsLoggedIn() {
			if err := client.Logout(ctx); err != nil {
				remoteUnconfirmed = true
				m.logger.Warnf("WhatsApp remote logout failed: %v. Check Linked Devices on your phone.", err)
			}
		}
		client.Disconnect()
	}

	devices, err := m.container.GetAllDevices(ctx)
	if err != nil {
		return false, fmt.Errorf("list local WhatsApp credentials: %w", err)
	}
	remoteUnconfirmed = remoteUnconfirmed || len(devices) != 0
	for _, device := range devices {
		if err := device.Delete(ctx); err != nil {
			return false, fmt.Errorf("remove local WhatsApp credentials: %w", err)
		}
	}

	if _, err := m.db.ExecContext(ctx, "UPDATE whatsapp_links SET jid = NULL WHERE id = 1"); err != nil {
		return false, fmt.Errorf("detach linked device: %w", err)
	}
	if keepHistory {
		return remoteUnconfirmed, nil
	}
	if m.feed != nil {
		f := m.feed
		f.mu.Lock()
		defer f.mu.Unlock()
		if err := m.store.ClearHistory(ctx); err != nil {
			return false, err
		}
		f.entries = nil
		clear(f.selected)
		f.epoch, f.seq, f.evicted = newEpoch(), 0, 0
		_, err := m.db.ExecContext(ctx, "DELETE FROM whatsapp_links WHERE id = 1")
		return remoteUnconfirmed, err
	}
	if m.mediaDir == "" {
		if err := m.store.ClearHistory(ctx); err != nil {
			return false, fmt.Errorf("clear in-memory history: %w", err)
		}
		_, err := m.db.ExecContext(ctx, "DELETE FROM whatsapp_links WHERE id = 1")
		return remoteUnconfirmed, err
	}
	if err := os.RemoveAll(filepath.Join(m.mediaDir, "downloads")); err != nil {
		return false, fmt.Errorf("clear downloaded media: %w", err)
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM chats"); err != nil {
		return false, fmt.Errorf("clear chat history: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM whatsapp_links WHERE id = 1"); err != nil {
		return false, fmt.Errorf("release account binding: %w", err)
	}
	return remoteUnconfirmed, tx.Commit()
}

func (m *Manager) getClientLocked(ctx context.Context) (*whatsmeow.Client, error) {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()
	if client != nil {
		return client, nil
	}

	_, jid, err := m.binding(ctx)
	if err != nil {
		return nil, err
	}
	if jid == "" {
		return nil, ErrNotLinked
	}
	parsed, err := types.ParseJID(jid)
	if err != nil {
		return nil, fmt.Errorf("invalid linked device JID: %w", err)
	}
	device, err := m.container.GetDevice(ctx, parsed)
	if err != nil {
		return nil, fmt.Errorf("load linked device: %w", err)
	}
	if device == nil {
		return nil, fmt.Errorf("linked device credentials are missing; call whatsapp_logout and relink")
	}
	client = whatsmeow.NewClient(device, m.logger)
	client.InitialAutoReconnect = true
	client.BackgroundEventCtx = m.ctx
	m.registerHandlers(client)
	m.mu.Lock()
	m.client = client
	m.mu.Unlock()
	if err := client.Connect(); err != nil {
		m.mu.Lock()
		if m.client == client {
			m.client = nil
		}
		m.mu.Unlock()
		return nil, fmt.Errorf("connect linked device: %w", err)
	}
	return client, nil
}
