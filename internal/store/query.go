package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type ListMessagesFilter struct {
	After             *string
	Before            *string
	SenderPhoneNumber *string
	ChatJID           *string
	Query             *string
	Limit             int
	Page              int
}

// ListMessages returns messages matching the given filter, newest first.
func (s *Store) ListMessages(ctx context.Context, f ListMessagesFilter) ([]Message, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	page := f.Page
	if page < 0 {
		page = 0
	}

	clauses := []string{"TRUE"}
	args := []any{}

	if f.After != nil && *f.After != "" {
		t, err := parseTimestamp(*f.After)
		if err != nil {
			return nil, fmt.Errorf("invalid after: %w", err)
		}
		args = append(args, t)
		clauses = append(clauses, fmt.Sprintf("messages.timestamp > $%d", len(args)))
	}
	if f.Before != nil && *f.Before != "" {
		t, err := parseTimestamp(*f.Before)
		if err != nil {
			return nil, fmt.Errorf("invalid before: %w", err)
		}
		args = append(args, t)
		clauses = append(clauses, fmt.Sprintf("messages.timestamp < $%d", len(args)))
	}
	if f.SenderPhoneNumber != nil && *f.SenderPhoneNumber != "" {
		args = append(args, *f.SenderPhoneNumber)
		clauses = append(clauses, fmt.Sprintf("messages.sender = $%d", len(args)))
	}
	if f.ChatJID != nil && *f.ChatJID != "" {
		args = append(args, *f.ChatJID)
		clauses = append(clauses, fmt.Sprintf("messages.chat_jid = $%d", len(args)))
	}
	if f.Query != nil && *f.Query != "" {
		args = append(args, "%"+*f.Query+"%")
		clauses = append(clauses, fmt.Sprintf("LOWER(messages.content) LIKE LOWER($%d)", len(args)))
	}

	args = append(args, limit)
	limitPlaceholder := fmt.Sprintf("$%d", len(args))
	args = append(args, page*limit)
	offsetPlaceholder := fmt.Sprintf("$%d", len(args))

	q := fmt.Sprintf(`
		SELECT messages.timestamp, messages.sender, chats.name, messages.content,
		       messages.is_from_me, chats.jid, messages.id, messages.media_type
		FROM messages
		JOIN chats ON messages.chat_jid = chats.jid
		WHERE %s
		ORDER BY messages.timestamp DESC
		LIMIT %s OFFSET %s
	`, strings.Join(clauses, " AND "), limitPlaceholder, offsetPlaceholder)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list messages: %w", err)
	}
	defer rows.Close()

	var result []Message
	for rows.Next() {
		var m Message
		var chatName, mediaType sql.NullString
		if err := rows.Scan(&m.Timestamp, &m.Sender, &chatName, &m.Content, &m.IsFromMe, &m.ChatJID, &m.ID, &mediaType); err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		m.ChatName = chatName.String
		m.MediaType = mediaType.String
		result = append(result, m)
	}
	return result, rows.Err()
}

// timestampLayouts are the ISO-8601 forms accepted for the after/before
// filters. Times without a UTC offset are taken to be UTC.
var timestampLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04Z07:00",
	"2006-01-02T15:04",
	"2006-01-02 15:04",
	"2006-01-02",
}

// parseTimestamp parses a filter timestamp into a time.Time, so it is bound
// as a real timestamp rather than compared as text — required for SQLite,
// which stores timestamps as text.
func parseTimestamp(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range timestampLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not an ISO-8601 timestamp (e.g. 2006-01-02T15:04:05Z)", s)
}

// GetMessageContext returns a message along with the messages immediately
// before and after it in the same chat.
func (s *Store) GetMessageContext(ctx context.Context, messageID string, before, after int) (*MessageContext, error) {
	var target Message
	var chatJID string
	var chatName, mediaType sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT messages.timestamp, messages.sender, chats.name, messages.content,
		       messages.is_from_me, chats.jid, messages.id, messages.chat_jid, messages.media_type
		FROM messages
		JOIN chats ON messages.chat_jid = chats.jid
		WHERE messages.id = $1
		LIMIT 1
	`, messageID).Scan(&target.Timestamp, &target.Sender, &chatName, &target.Content,
		&target.IsFromMe, &target.ChatJID, &target.ID, &chatJID, &mediaType)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("message with ID %s not found", messageID)
	} else if err != nil {
		return nil, fmt.Errorf("failed to look up message: %w", err)
	}
	target.ChatName = chatName.String
	target.MediaType = mediaType.String

	before_, err := s.messagesAround(ctx, chatJID, target.Timestamp, before, true)
	if err != nil {
		return nil, err
	}
	after_, err := s.messagesAround(ctx, chatJID, target.Timestamp, after, false)
	if err != nil {
		return nil, err
	}

	return &MessageContext{Message: target, Before: before_, After: after_}, nil
}

func (s *Store) messagesAround(ctx context.Context, chatJID string, ts any, limit int, beforeTarget bool) ([]Message, error) {
	op, order := "<", "DESC"
	if !beforeTarget {
		op, order = ">", "ASC"
	}
	q := fmt.Sprintf(`
		SELECT messages.timestamp, messages.sender, chats.name, messages.content,
		       messages.is_from_me, chats.jid, messages.id, messages.media_type
		FROM messages
		JOIN chats ON messages.chat_jid = chats.jid
		WHERE messages.chat_jid = $1 AND messages.timestamp %s $2
		ORDER BY messages.timestamp %s
		LIMIT $3
	`, op, order)

	rows, err := s.db.QueryContext(ctx, q, chatJID, ts, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list surrounding messages: %w", err)
	}
	defer rows.Close()

	var result []Message
	for rows.Next() {
		var m Message
		var chatName, mediaType sql.NullString
		if err := rows.Scan(&m.Timestamp, &m.Sender, &chatName, &m.Content, &m.IsFromMe, &m.ChatJID, &m.ID, &mediaType); err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		m.ChatName = chatName.String
		m.MediaType = mediaType.String
		result = append(result, m)
	}
	return result, rows.Err()
}

// ListChats returns chats matching an optional name/JID search term.
func (s *Store) ListChats(ctx context.Context, query string, limit, page int, includeLastMessage bool, sortByName bool) ([]Chat, error) {
	if limit <= 0 {
		limit = 20
	}
	if page < 0 {
		page = 0
	}

	join := ""
	columns := "NULL, NULL, NULL"
	if includeLastMessage {
		columns = "messages.content, messages.sender, messages.is_from_me"
		join = `LEFT JOIN messages ON chats.jid = messages.chat_jid
		        AND chats.last_message_time = messages.timestamp`
	}

	clauses := []string{"TRUE"}
	args := []any{}
	if query != "" {
		args = append(args, "%"+query+"%")
		clauses = append(clauses, fmt.Sprintf("(LOWER(chats.name) LIKE LOWER($%d) OR chats.jid LIKE $%d)", len(args), len(args)))
	}

	orderBy := "chats.last_message_time DESC NULLS LAST"
	if sortByName {
		orderBy = "chats.name"
	}

	args = append(args, limit)
	limitPlaceholder := fmt.Sprintf("$%d", len(args))
	args = append(args, page*limit)
	offsetPlaceholder := fmt.Sprintf("$%d", len(args))

	q := fmt.Sprintf(`
		SELECT chats.jid, chats.name, chats.last_message_time,
		       %s
		FROM chats
		%s
		WHERE %s
		ORDER BY %s
		LIMIT %s OFFSET %s
	`, columns, join, strings.Join(clauses, " AND "), orderBy, limitPlaceholder, offsetPlaceholder)

	return s.scanChats(ctx, q, args...)
}

// SearchContacts finds known direct-chat contacts by name or phone number.
func (s *Store) SearchContacts(ctx context.Context, query string) ([]Contact, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT jid, name
		FROM chats
		WHERE (LOWER(name) LIKE LOWER($1) OR LOWER(jid) LIKE LOWER($1))
		  AND jid NOT LIKE '%@g.us'
		ORDER BY name, jid
		LIMIT 50
	`, "%"+query+"%")
	if err != nil {
		return nil, fmt.Errorf("failed to search contacts: %w", err)
	}
	defer rows.Close()

	var result []Contact
	for rows.Next() {
		var jid string
		var name sql.NullString
		if err := rows.Scan(&jid, &name); err != nil {
			return nil, fmt.Errorf("failed to scan contact: %w", err)
		}
		result = append(result, Contact{
			JID:         jid,
			Name:        name.String,
			PhoneNumber: strings.SplitN(jid, "@", 2)[0],
		})
	}
	return result, rows.Err()
}

// GetContactChats returns every chat a contact has sent to or that matches their JID.
func (s *Store) GetContactChats(ctx context.Context, jid string, limit, page int) ([]Chat, error) {
	if limit <= 0 {
		limit = 20
	}
	if page < 0 {
		page = 0
	}
	q := `
		SELECT DISTINCT c.jid, c.name, c.last_message_time, m.content, m.sender, m.is_from_me
		FROM chats c
		JOIN messages m ON c.jid = m.chat_jid
		WHERE (m.sender = $1 OR c.jid = $1)
		ORDER BY c.last_message_time DESC NULLS LAST
		LIMIT $2 OFFSET $3
	`
	return s.scanChats(ctx, q, jid, limit, page*limit)
}

// GetLastInteraction returns the most recent message sent by or to a contact.
func (s *Store) GetLastInteraction(ctx context.Context, jid string) (*Message, error) {
	var m Message
	var chatName, mediaType sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT m.timestamp, m.sender, c.name, m.content, m.is_from_me, c.jid, m.id, m.media_type
		FROM messages m
		JOIN chats c ON m.chat_jid = c.jid
		WHERE (m.sender = $1 OR c.jid = $1)
		ORDER BY m.timestamp DESC
		LIMIT 1
	`, jid).Scan(&m.Timestamp, &m.Sender, &chatName, &m.Content, &m.IsFromMe, &m.ChatJID, &m.ID, &mediaType)
	if err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("failed to look up last interaction: %w", err)
	}
	m.ChatName = chatName.String
	m.MediaType = mediaType.String
	return &m, nil
}

// GetChat returns metadata for a single chat by JID.
func (s *Store) GetChat(ctx context.Context, chatJID string, includeLastMessage bool) (*Chat, error) {
	join := ""
	columns := "NULL, NULL, NULL"
	if includeLastMessage {
		columns = "m.content, m.sender, m.is_from_me"
		join = `LEFT JOIN messages m ON c.jid = m.chat_jid
		        AND c.last_message_time = m.timestamp`
	}
	q := fmt.Sprintf(`
		SELECT c.jid, c.name, c.last_message_time, %s
		FROM chats c
		%s
		WHERE c.jid = $1
	`, columns, join)

	chats, err := s.scanChats(ctx, q, chatJID)
	if err != nil {
		return nil, err
	}
	if len(chats) == 0 {
		return nil, nil
	}
	return &chats[0], nil
}

// GetDirectChatByContact finds a non-group chat whose JID contains the given phone number.
func (s *Store) GetDirectChatByContact(ctx context.Context, phoneNumber string) (*Chat, error) {
	q := `
		SELECT c.jid, c.name, c.last_message_time, m.content, m.sender, m.is_from_me
		FROM chats c
		LEFT JOIN messages m ON c.jid = m.chat_jid
		     AND c.last_message_time = m.timestamp
		WHERE c.jid LIKE $1 AND c.jid NOT LIKE '%@g.us'
		LIMIT 1
	`
	chats, err := s.scanChats(ctx, q, "%"+phoneNumber+"%")
	if err != nil {
		return nil, err
	}
	if len(chats) == 0 {
		return nil, nil
	}
	return &chats[0], nil
}

func (s *Store) scanChats(ctx context.Context, query string, args ...any) ([]Chat, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query chats: %w", err)
	}
	defer rows.Close()

	var result []Chat
	for rows.Next() {
		var c Chat
		var name, lastMessage, lastSender sql.NullString
		var lastMessageTime sql.NullTime
		var lastIsFromMe sql.NullBool
		if err := rows.Scan(&c.JID, &name, &lastMessageTime, &lastMessage, &lastSender, &lastIsFromMe); err != nil {
			return nil, fmt.Errorf("failed to scan chat: %w", err)
		}
		c.Name = name.String
		if lastMessageTime.Valid {
			t := lastMessageTime.Time
			c.LastMessageTime = &t
		}
		if lastMessage.Valid {
			c.LastMessage = &lastMessage.String
		}
		if lastSender.Valid {
			c.LastSender = &lastSender.String
		}
		if lastIsFromMe.Valid {
			c.LastIsFromMe = &lastIsFromMe.Bool
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// GetSenderName resolves a human-readable name for a sender JID by looking
// up the chat with that JID, falling back to a partial match on the JID's
// phone-number part, and finally the raw JID itself.
func (s *Store) GetSenderName(ctx context.Context, senderJID string) string {
	var name sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT name FROM chats WHERE jid = $1 LIMIT 1`, senderJID).Scan(&name)
	if err == nil && name.String != "" {
		return name.String
	}

	phonePart := senderJID
	if idx := strings.Index(senderJID, "@"); idx >= 0 {
		phonePart = senderJID[:idx]
	}
	err = s.db.QueryRowContext(ctx, `SELECT name FROM chats WHERE jid LIKE $1 LIMIT 1`, "%"+phonePart+"%").Scan(&name)
	if err == nil && name.String != "" {
		return name.String
	}

	return senderJID
}
