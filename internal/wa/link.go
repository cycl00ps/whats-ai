package wa

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// LinkDevice starts or polls a device-linking session for a user.
//
// If the user already has a linked device, it reports that immediately.
// Otherwise it starts a new pairing session (or returns the state of one
// already in progress): with no phone number, a QR code is generated that
// refreshes every ~20-60 seconds; with a phone number, an 8-character
// pairing code is generated for entry into WhatsApp's
// Settings > Linked Devices > Link with phone number.
//
// Callers should re-invoke this (e.g. as a repeated MCP tool call) until
// Linked is true, refreshing the QR code each time it has gone stale.
func (m *Manager) LinkDevice(ctx context.Context, phoneNumber string) (*LinkState, error) {
	m.lifecycle.Lock()
	defer m.lifecycle.Unlock()
	account, jid, err := m.binding(ctx)
	if err != nil {
		return nil, err
	}
	if jid != "" {
		return &LinkState{Linked: true, JID: jid}, nil
	}

	m.mu.Lock()
	if pending := m.pending; pending != nil {
		if pending.cancelled {
			m.mu.Unlock()
			return nil, fmt.Errorf("retry whatsapp_logout to finish cancelling pairing")
		}
		state := pending.snapshot()
		if state.Error != nil {
			m.pending = nil
		}
		m.mu.Unlock()
		return &state, nil
	}
	m.mu.Unlock()
	// Discard credentials left by a process that stopped before recording a link.
	devices, err := m.container.GetAllDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list unlinked WhatsApp credentials: %w", err)
	}
	for _, device := range devices {
		if err := device.Delete(ctx); err != nil {
			return nil, fmt.Errorf("remove unlinked WhatsApp credentials: %w", err)
		}
	}
	client := whatsmeow.NewClient(m.container.NewDevice(), m.logger)
	client.BackgroundEventCtx = m.ctx
	m.mu.Lock()
	pending := &pendingLink{client: client}
	m.pending = pending
	m.mu.Unlock()
	m.registerHandlers(client)
	client.PrePairCallback = func(jid types.JID, _, _ string) bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.pending != pending || pending.cancelled || pending.pairDone != nil {
			return false
		}
		if account != "" && account != jid.ToNonAD().String() {
			pending.setErr(fmt.Errorf("history belongs to another WhatsApp account; run whatsapp_logout to clear it before switching"))
			return false
		}
		pending.pairDone = make(chan struct{})
		return true
	}
	client.AddEventHandler(func(evt any) {
		switch failure := evt.(type) {
		case *events.PairError:
			if pending.snapshot().Error == nil {
				pending.setErr(failure.Error)
			}
		case *events.PairSuccess:
		default:
			return
		}
		m.mu.Lock()
		if pending.pairDone != nil {
			select {
			case <-pending.pairDone:
			default:
				close(pending.pairDone)
			}
		}
		m.mu.Unlock()
	})

	bgCtx := m.ctx

	if phoneNumber == "" {
		qrChan, err := client.GetQRChannel(bgCtx)
		if err != nil {
			m.clearPending(pending)
			return nil, fmt.Errorf("failed to start QR pairing: %w", err)
		}
		if err := client.Connect(); err != nil {
			m.clearPending(pending)
			return nil, fmt.Errorf("failed to connect: %w", err)
		}

		go func() {
			for evt := range qrChan {
				switch evt.Event {
				case "code":
					pending.setQR(evt.Code, time.Now().Add(evt.Timeout))
				case "timeout":
					pending.setErr(fmt.Errorf("QR code timed out, call link_whatsapp again to get a new one"))
					return
				case "success":
					return
				}
			}
		}()
	} else {
		if err := client.Connect(); err != nil {
			m.clearPending(pending)
			return nil, fmt.Errorf("failed to connect: %w", err)
		}

		code, err := client.PairPhone(bgCtx, phoneNumber, true, whatsmeow.PairClientChrome, "WhatsApp MCP")
		if err != nil {
			m.clearPending(pending)
			client.Disconnect()
			return nil, fmt.Errorf("failed to request pairing code: %w", err)
		}
		pending.setPairCode(code)
	}

	return waitForFirstUpdate(pending), nil
}

func (m *Manager) clearPending(pending *pendingLink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == pending {
		m.pending = nil
	}
}

func (m *Manager) onLinked(ctx context.Context, client *whatsmeow.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil || m.pending.client != client || m.pending.cancelled || client.Store.ID == nil {
		return
	}
	jid := *client.Store.ID
	result, err := m.db.ExecContext(ctx, `INSERT INTO whatsapp_links (id, account_jid, jid)
		VALUES (1, $1, $2)
		ON CONFLICT (id) DO UPDATE SET jid = EXCLUDED.jid
		WHERE whatsapp_links.account_jid = EXCLUDED.account_jid AND whatsapp_links.jid IS NULL`,
		jid.ToNonAD().String(), jid.String())
	if err == nil {
		var count int64
		count, err = result.RowsAffected()
		if err == nil && count != 1 {
			err = fmt.Errorf("account binding changed during pairing")
		}
	}
	if err != nil {
		m.pending.setErr(fmt.Errorf("failed to save linked device: %w", err))
		go client.Disconnect()
		return
	}
	m.client = client
	m.pending = nil
}

// waitForFirstUpdate briefly waits for the first QR code or an error to
// become available, so the initial link_whatsapp call doesn't come back
// empty while the goroutine above is still starting up.
func waitForFirstUpdate(pending *pendingLink) *LinkState {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state := pending.snapshot()
		if state.QRCode != "" || state.PairCode != "" || state.Error != nil {
			return &state
		}
		time.Sleep(200 * time.Millisecond)
	}
	state := pending.snapshot()
	return &state
}
