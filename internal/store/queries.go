package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/eddmann/whatsapp-mcp/internal/domain"
)

// CountChats returns the total number of chats matching the query.
func (d *DB) CountChats(query string) (int, error) {
	q := "SELECT COUNT(*) FROM chats"
	args := []any{}

	if query != "" {
		q += " WHERE (LOWER(name) LIKE LOWER(?) OR jid LIKE ?)"
		args = append(args, "%"+query+"%", "%"+query+"%")
	}

	var count int
	err := d.Messages.QueryRow(q, args...).Scan(&count)
	return count, err
}

// ListChats returns chats with optional filtering, pagination and sorting.
func (d *DB) ListChats(opts domain.ListChatsOptions) ([]domain.Chat, error) {
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	if opts.Page < 0 {
		opts.Page = 0
	}

	orderBy := "chats.last_message_time DESC"
	if opts.SortBy == "name" {
		orderBy = "chats.name"
	}

	var q string
	if opts.IncludeLastMessage {
		q = `SELECT
			chats.jid,
			chats.name,
			chats.last_message_time,
			m.content AS last_message,
			m.sender AS last_sender,
			m.is_from_me AS last_is_from_me
		FROM chats
		LEFT JOIN messages m ON chats.jid = m.chat_jid AND chats.last_message_time = m.timestamp`
	} else {
		q = `SELECT chats.jid, chats.name, chats.last_message_time FROM chats`
	}

	args := []any{}
	if opts.Query != "" {
		q += " WHERE (LOWER(chats.name) LIKE LOWER(?) OR chats.jid LIKE ?)"
		args = append(args, "%"+opts.Query+"%", "%"+opts.Query+"%")
	}
	q += fmt.Sprintf(" ORDER BY %s LIMIT ? OFFSET ?", orderBy)
	args = append(args, opts.Limit, opts.Page*opts.Limit)

	rows, err := d.Messages.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chats []domain.Chat
	for rows.Next() {
		var chat domain.Chat
		var name, ts sql.NullString

		if opts.IncludeLastMessage {
			var lastMsg, lastSender sql.NullString
			var lastFromMe sql.NullBool
			if err := rows.Scan(&chat.JID, &name, &ts, &lastMsg, &lastSender, &lastFromMe); err != nil {
				return nil, err
			}
			if lastMsg.Valid {
				chat.LastMessage = &lastMsg.String
			}
			if lastSender.Valid {
				chat.LastSender = &lastSender.String
			}
			if lastFromMe.Valid {
				chat.LastIsFromMe = &lastFromMe.Bool
			}
		} else {
			if err := rows.Scan(&chat.JID, &name, &ts); err != nil {
				return nil, err
			}
		}

		if name.Valid {
			chat.Name = &name.String
		}
		if ts.Valid {
			t, _ := time.Parse(time.RFC3339, ts.String)
			chat.LastMessageTime = &t
		}

		// Determine if this is a group chat
		chat.IsGroup = strings.HasSuffix(chat.JID, "@g.us")

		chats = append(chats, chat)
	}

	return chats, nil
}

// SearchContacts searches for contacts by name or JID (excludes groups).
func (d *DB) SearchContacts(query string) ([]domain.Contact, error) {
	pattern := "%" + strings.ToLower(query) + "%"
	rows, err := d.Messages.Query(`
		SELECT DISTINCT jid, name FROM chats
		WHERE (LOWER(name) LIKE ? OR LOWER(jid) LIKE ?) AND jid NOT LIKE '%@g.us'
		ORDER BY name, jid LIMIT 50`, pattern, pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contacts []domain.Contact
	for rows.Next() {
		var jid string
		var name sql.NullString
		if err := rows.Scan(&jid, &name); err != nil {
			return nil, err
		}

		contact := domain.Contact{
			JID:   jid,
			Phone: strings.Split(jid, "@")[0],
		}
		if name.Valid {
			contact.Name = &name.String
		}
		contacts = append(contacts, contact)
	}

	return contacts, nil
}

// GetChat retrieves a single chat by JID.
func (d *DB) GetChat(chatJID string, includeLast bool) (*domain.Chat, error) {
	row := d.Messages.QueryRow(`SELECT c.jid, c.name, c.last_message_time FROM chats c WHERE c.jid = ?`, chatJID)
	var jid string
	var name, ts sql.NullString
	if err := row.Scan(&jid, &name, &ts); err != nil {
		return nil, err
	}

	chat := &domain.Chat{JID: jid}
	if name.Valid {
		chat.Name = &name.String
	}
	if ts.Valid {
		t, _ := time.Parse(time.RFC3339, ts.String)
		chat.LastMessageTime = &t
	}

	// Determine if this is a group chat
	chat.IsGroup = strings.HasSuffix(chat.JID, "@g.us")

	if includeLast {
		r := d.Messages.QueryRow(`SELECT content, sender, is_from_me FROM messages WHERE chat_jid = ? ORDER BY timestamp DESC LIMIT 1`, chatJID)
		var content, sender sql.NullString
		var isFromMe sql.NullBool
		_ = r.Scan(&content, &sender, &isFromMe)
		if content.Valid {
			chat.LastMessage = &content.String
		}
		if sender.Valid {
			chat.LastSender = &sender.String
		}
		if isFromMe.Valid {
			chat.LastIsFromMe = &isFromMe.Bool
		}
	}

	return chat, nil
}

// ListMessages lists messages with filters and pagination.
func (d *DB) ListMessages(opts domain.ListMessagesOptions) ([]domain.Message, error) {
	parts := []string{"SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type FROM messages JOIN chats ON messages.chat_jid = chats.jid"}
	where := []string{}
	args := []any{}

	if opts.After != "" {
		where = append(where, "messages.timestamp > ?")
		args = append(args, opts.After)
	}
	if opts.Before != "" {
		where = append(where, "messages.timestamp < ?")
		args = append(args, opts.Before)
	}
	if opts.Sender != "" {
		where = append(where, "messages.sender = ?")
		args = append(args, opts.Sender)
	}
	if opts.ChatJID != "" {
		where = append(where, "messages.chat_jid = ?")
		args = append(args, opts.ChatJID)
	}
	if opts.Query != "" {
		where = append(where, "LOWER(messages.content) LIKE LOWER(?)")
		args = append(args, "%"+opts.Query+"%")
	}

	if len(where) > 0 {
		parts = append(parts, "WHERE "+strings.Join(where, " AND "))
	}

	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	if opts.Page < 0 {
		opts.Page = 0
	}

	parts = append(parts, "ORDER BY messages.timestamp DESC", "LIMIT ? OFFSET ?")
	args = append(args, opts.Limit, opts.Page*opts.Limit)

	rows, err := d.Messages.Query(strings.Join(parts, " "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []domain.Message
	for rows.Next() {
		msg, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}

	// Context expansion if requested
	if opts.IncludeContext && len(messages) > 0 {
		expanded := make([]domain.Message, 0, len(messages)*(2+opts.ContextBefore+opts.ContextAfter))
		for _, base := range messages {
			expanded = append(expanded, base)

			// Fetch before
			if opts.ContextBefore > 0 {
				beforeRows, err := d.Messages.Query(`SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type FROM messages JOIN chats ON messages.chat_jid = chats.jid WHERE messages.chat_jid = ? AND messages.timestamp < ? ORDER BY messages.timestamp DESC LIMIT ?`, base.ChatJID, base.Timestamp.Format(time.RFC3339), opts.ContextBefore)
				if err == nil {
					for beforeRows.Next() {
						msg, err := scanMessage(beforeRows)
						if err == nil {
							expanded = append(expanded, msg)
						}
					}
					beforeRows.Close()
				}
			}

			// Fetch after
			if opts.ContextAfter > 0 {
				afterRows, err := d.Messages.Query(`SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type FROM messages JOIN chats ON messages.chat_jid = chats.jid WHERE messages.chat_jid = ? AND messages.timestamp > ? ORDER BY messages.timestamp ASC LIMIT ?`, base.ChatJID, base.Timestamp.Format(time.RFC3339), opts.ContextAfter)
				if err == nil {
					for afterRows.Next() {
						msg, err := scanMessage(afterRows)
						if err == nil {
							expanded = append(expanded, msg)
						}
					}
					afterRows.Close()
				}
			}
		}
		messages = expanded
	}

	return messages, nil
}

// GetMessageContext retrieves messages before and after a specific message.
func (d *DB) GetMessageContext(messageID string, before, after int) (*domain.MessageContext, error) {
	if before <= 0 {
		before = 5
	}
	if after <= 0 {
		after = 5
	}

	row := d.Messages.QueryRow(`SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type FROM messages JOIN chats ON messages.chat_jid = chats.jid WHERE messages.id = ?`, messageID)
	currentMsg, err := scanMessage(row)
	if err != nil {
		return nil, err
	}

	beforeRows, err := d.Messages.Query(`SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type FROM messages JOIN chats ON messages.chat_jid = chats.jid WHERE messages.chat_jid = ? AND messages.timestamp < ? ORDER BY messages.timestamp DESC LIMIT ?`, currentMsg.ChatJID, currentMsg.Timestamp.Format(time.RFC3339), before)
	if err != nil {
		return nil, err
	}
	defer beforeRows.Close()

	var beforeMsgs []domain.Message
	for beforeRows.Next() {
		msg, err := scanMessage(beforeRows)
		if err != nil {
			return nil, err
		}
		beforeMsgs = append(beforeMsgs, msg)
	}

	afterRows, err := d.Messages.Query(`SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type FROM messages JOIN chats ON messages.chat_jid = chats.jid WHERE messages.chat_jid = ? AND messages.timestamp > ? ORDER BY messages.timestamp ASC LIMIT ?`, currentMsg.ChatJID, currentMsg.Timestamp.Format(time.RFC3339), after)
	if err != nil {
		return nil, err
	}
	defer afterRows.Close()

	var afterMsgs []domain.Message
	for afterRows.Next() {
		msg, err := scanMessage(afterRows)
		if err != nil {
			return nil, err
		}
		afterMsgs = append(afterMsgs, msg)
	}

	return &domain.MessageContext{
		Message: currentMsg,
		Before:  beforeMsgs,
		After:   afterMsgs,
	}, nil
}

// GetDirectChatByContact retrieves a direct chat by phone number.
func (d *DB) GetDirectChatByContact(phone string) (*domain.Chat, error) {
	row := d.Messages.QueryRow(`SELECT
		c.jid,
		c.name,
		c.last_message_time,
		m.content AS last_message,
		m.sender AS last_sender,
		m.is_from_me AS last_is_from_me
	FROM chats c
	LEFT JOIN messages m ON c.jid = m.chat_jid AND c.last_message_time = m.timestamp
	WHERE c.jid LIKE ? AND c.jid NOT LIKE '%@g.us' LIMIT 1`, "%"+phone+"%")

	var jid string
	var name, ts sql.NullString
	var lastMsg, lastSender sql.NullString
	var lastFromMe sql.NullBool

	if err := row.Scan(&jid, &name, &ts, &lastMsg, &lastSender, &lastFromMe); err != nil {
		return nil, err
	}

	chat := &domain.Chat{JID: jid}
	if name.Valid {
		chat.Name = &name.String
	}
	if ts.Valid {
		t, _ := time.Parse(time.RFC3339, ts.String)
		chat.LastMessageTime = &t
	}
	if lastMsg.Valid {
		chat.LastMessage = &lastMsg.String
	}
	if lastSender.Valid {
		chat.LastSender = &lastSender.String
	}
	if lastFromMe.Valid {
		chat.LastIsFromMe = &lastFromMe.Bool
	}

	// Determine if this is a group chat
	chat.IsGroup = strings.HasSuffix(chat.JID, "@g.us")

	return chat, nil
}

// GetContactChats retrieves chats involving a contact.
func (d *DB) GetContactChats(jid string, limit, page int) ([]domain.Chat, error) {
	if limit <= 0 {
		limit = 20
	}
	if page < 0 {
		page = 0
	}

	rows, err := d.Messages.Query(`
		SELECT DISTINCT
			c.jid,
			c.name,
			c.last_message_time,
			lm.content AS last_message,
			lm.sender AS last_sender,
			lm.is_from_me AS last_is_from_me
		FROM chats c
		LEFT JOIN messages lm ON c.jid = lm.chat_jid AND c.last_message_time = lm.timestamp
		WHERE EXISTS (SELECT 1 FROM messages m WHERE m.chat_jid = c.jid AND (m.sender = ? OR c.jid = ?))
		ORDER BY c.last_message_time DESC LIMIT ? OFFSET ?
	`, jid, jid, limit, page*limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chats []domain.Chat
	for rows.Next() {
		var chat domain.Chat
		var name, ts sql.NullString
		var lastMsg, lastSender sql.NullString
		var lastFromMe sql.NullBool

		if err := rows.Scan(&chat.JID, &name, &ts, &lastMsg, &lastSender, &lastFromMe); err != nil {
			return nil, err
		}

		if name.Valid {
			chat.Name = &name.String
		}
		if ts.Valid {
			t, _ := time.Parse(time.RFC3339, ts.String)
			chat.LastMessageTime = &t
		}
		if lastMsg.Valid {
			chat.LastMessage = &lastMsg.String
		}
		if lastSender.Valid {
			chat.LastSender = &lastSender.String
		}
		if lastFromMe.Valid {
			chat.LastIsFromMe = &lastFromMe.Bool
		}

		// Determine if this is a group chat
		chat.IsGroup = strings.HasSuffix(chat.JID, "@g.us")

		chats = append(chats, chat)
	}

	return chats, nil
}

// GetLastInteraction retrieves the most recent message involving a contact.
func (d *DB) GetLastInteraction(jid string) (*domain.Message, error) {
	row := d.Messages.QueryRow(`
		SELECT m.timestamp, m.sender, c.name, m.content, m.is_from_me, c.jid, m.id, m.media_type
		FROM messages m JOIN chats c ON m.chat_jid = c.jid
		WHERE m.sender = ? OR c.jid = ?
		ORDER BY m.timestamp DESC LIMIT 1`, jid, jid)

	msg, err := scanMessage(row)
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

// SearchMessages performs full-text search on message content.
func (d *DB) SearchMessages(opts domain.SearchMessagesOptions) ([]domain.Message, error) {
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	if opts.Page < 0 {
		opts.Page = 0
	}

	// Try FTS5
	rows, err := d.Messages.Query(`
		SELECT m.timestamp, m.sender, c.name, m.content, m.is_from_me, m.chat_jid, m.id, m.media_type
		FROM messages_fts f
		JOIN messages m ON m.rowid = f.rowid
		JOIN chats c ON m.chat_jid = c.jid
		WHERE messages_fts MATCH ?
		ORDER BY m.timestamp DESC
		LIMIT ? OFFSET ?`, opts.Query, opts.Limit, opts.Page*opts.Limit)

	if err != nil {
		// Fallback to LIKE
		rows, err = d.Messages.Query(`
			SELECT m.timestamp, m.sender, c.name, m.content, m.is_from_me, m.chat_jid, m.id, m.media_type
			FROM messages m JOIN chats c ON m.chat_jid = c.jid
			WHERE LOWER(m.content) LIKE LOWER(?)
			ORDER BY m.timestamp DESC
			LIMIT ? OFFSET ?`, "%"+opts.Query+"%", opts.Limit, opts.Page*opts.Limit)
		if err != nil {
			return nil, err
		}
	}
	defer rows.Close()

	var messages []domain.Message
	for rows.Next() {
		msg, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}

	return messages, nil
}

// scanMessage is a helper to scan a message from a row.
func scanMessage(scanner interface {
	Scan(dest ...any) error
}) (domain.Message, error) {
	var msg domain.Message
	var ts string
	var chatName, content, media sql.NullString

	if err := scanner.Scan(&ts, &msg.Sender, &chatName, &content, &msg.IsFromMe, &msg.ChatJID, &msg.ID, &media); err != nil {
		return msg, err
	}

	msg.Timestamp, _ = time.Parse(time.RFC3339, ts)
	if chatName.Valid {
		msg.ChatName = &chatName.String
	}
	if content.Valid {
		msg.Content = &content.String
	}
	if media.Valid {
		msg.MediaType = &media.String
	}

	return msg, nil
}
