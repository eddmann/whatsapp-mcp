package store

import (
	"database/sql"
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

// ListChats returns chats with filtering and pagination.
// Always sorted by last activity and includes last message preview.
func (d *DB) ListChats(opts domain.ListChatsOptions) ([]domain.Chat, error) {
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	if opts.Page < 0 {
		opts.Page = 0
	}

	q := `SELECT
		chats.jid,
		chats.name,
		chats.last_message_time,
		m.content AS last_message,
		m.sender AS last_sender,
		m.is_from_me AS last_is_from_me
	FROM chats
	LEFT JOIN messages m ON chats.jid = m.chat_jid AND chats.last_message_time = m.timestamp`

	where := []string{}
	args := []any{}

	if opts.Query != "" {
		where = append(where, "(LOWER(chats.name) LIKE LOWER(?) OR chats.jid LIKE ?)")
		args = append(args, "%"+opts.Query+"%", "%"+opts.Query+"%")
	}

	if opts.OnlyGroups {
		where = append(where, "chats.jid LIKE '%@g.us'")
	}

	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}

	q += " ORDER BY chats.last_message_time DESC LIMIT ? OFFSET ?"
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
		where = append(where, "datetime(messages.timestamp) > datetime(?)")
		args = append(args, opts.After)
	}
	if opts.Before != "" {
		where = append(where, "datetime(messages.timestamp) < datetime(?)")
		args = append(args, opts.Before)
	}
	if opts.ChatJID != "" {
		where = append(where, "messages.chat_jid = ?")
		args = append(args, opts.ChatJID)
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

	return messages, nil
}

// SearchMessages performs full-text search on message content.
func (d *DB) SearchMessages(opts domain.SearchMessagesOptions) ([]domain.Message, error) {
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	if opts.Page < 0 {
		opts.Page = 0
	}

	dateWhere := []string{}
	dateArgs := []any{}
	if opts.After != "" {
		dateWhere = append(dateWhere, "datetime(m.timestamp) > datetime(?)")
		dateArgs = append(dateArgs, opts.After)
	}
	if opts.Before != "" {
		dateWhere = append(dateWhere, "datetime(m.timestamp) < datetime(?)")
		dateArgs = append(dateArgs, opts.Before)
	}

	ftsQuery := `
		SELECT m.timestamp, m.sender, c.name, m.content, m.is_from_me, m.chat_jid, m.id, m.media_type
		FROM messages_fts f
		JOIN messages m ON m.rowid = f.rowid
		JOIN chats c ON m.chat_jid = c.jid
		WHERE messages_fts MATCH ?`

	ftsArgs := []any{opts.Query}
	if len(dateWhere) > 0 {
		ftsQuery += " AND " + strings.Join(dateWhere, " AND ")
		ftsArgs = append(ftsArgs, dateArgs...)
	}
	ftsQuery += " ORDER BY m.timestamp DESC LIMIT ? OFFSET ?"
	ftsArgs = append(ftsArgs, opts.Limit, opts.Page*opts.Limit)

	rows, err := d.Messages.Query(ftsQuery, ftsArgs...)

	if err != nil {
		likeQuery := `
			SELECT m.timestamp, m.sender, c.name, m.content, m.is_from_me, m.chat_jid, m.id, m.media_type
			FROM messages m JOIN chats c ON m.chat_jid = c.jid
			WHERE LOWER(m.content) LIKE LOWER(?)`

		likeArgs := []any{"%" + opts.Query + "%"}
		if len(dateWhere) > 0 {
			likeQuery += " AND " + strings.Join(dateWhere, " AND ")
			likeArgs = append(likeArgs, dateArgs...)
		}
		likeQuery += " ORDER BY m.timestamp DESC LIMIT ? OFFSET ?"
		likeArgs = append(likeArgs, opts.Limit, opts.Page*opts.Limit)

		rows, err = d.Messages.Query(likeQuery, likeArgs...)
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

	if len(messages) > 0 {
		const contextSize = 2
		expanded := make([]domain.Message, 0, len(messages)*(1+2*contextSize))
		for _, base := range messages {
			expanded = append(expanded, base)

			beforeRows, err := d.Messages.Query(`SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type FROM messages JOIN chats ON messages.chat_jid = chats.jid WHERE messages.chat_jid = ? AND datetime(messages.timestamp) < datetime(?) ORDER BY messages.timestamp DESC LIMIT ?`, base.ChatJID, base.Timestamp.Format(time.RFC3339), contextSize)
			if err == nil {
				for beforeRows.Next() {
					msg, err := scanMessage(beforeRows)
					if err == nil {
						expanded = append(expanded, msg)
					}
				}
				beforeRows.Close()
			}

			afterRows, err := d.Messages.Query(`SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type FROM messages JOIN chats ON messages.chat_jid = chats.jid WHERE messages.chat_jid = ? AND datetime(messages.timestamp) > datetime(?) ORDER BY messages.timestamp ASC LIMIT ?`, base.ChatJID, base.Timestamp.Format(time.RFC3339), contextSize)
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
		messages = expanded
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

// GetActiveChats returns chats with activity in the specified time range.
func (d *DB) GetActiveChats(after, before string, onlyGroups bool, limit int) ([]domain.ActiveChatInfo, error) {
	query := `
		SELECT
			c.jid,
			c.name,
			COUNT(m.id) as msg_count,
			MAX(m.timestamp) as last_time,
			c.last_message_time
		FROM chats c
		JOIN messages m ON c.jid = m.chat_jid
		WHERE datetime(m.timestamp) > datetime(?) AND datetime(m.timestamp) < datetime(?)
	`

	args := []any{after, before}

	// Apply groups-only filter
	if onlyGroups {
		query += " AND c.jid LIKE '%@g.us'"
	}

	query += " GROUP BY c.jid, c.name ORDER BY last_time DESC LIMIT ?"
	args = append(args, limit)

	rows, err := d.Messages.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chats []domain.ActiveChatInfo
	for rows.Next() {
		var chat domain.ActiveChatInfo
		var name sql.NullString
		var lastTimeStr, lastMsgTimeStr string

		if err := rows.Scan(&chat.ChatJID, &name, &chat.MessageCount, &lastTimeStr, &lastMsgTimeStr); err != nil {
			continue
		}

		if name.Valid {
			chat.ChatName = name.String
		} else {
			chat.ChatName = chat.ChatJID
		}

		chat.IsGroup = strings.Contains(chat.ChatJID, "@g.us")
		chat.LastMessageTime, _ = time.Parse(time.RFC3339, lastTimeStr)

		var content sql.NullString
		var isFromMe bool
		d.Messages.QueryRow(`
			SELECT content, is_from_me
			FROM messages
			WHERE chat_jid = ?
			ORDER BY timestamp DESC LIMIT 1
		`, chat.ChatJID).Scan(&content, &isFromMe)

		if content.Valid {
			chat.LastMessageText = &content.String
		}
		chat.LastIsFromMe = isFromMe

		chats = append(chats, chat)
	}

	return chats, nil
}

// GetQuestionsForMe finds messages ending with '?' where is_from_me = false.
func (d *DB) GetQuestionsForMe(after, before string, limit int) ([]domain.Message, error) {
	query := `
		SELECT m.timestamp, m.sender, c.name, m.content, m.is_from_me, m.chat_jid, m.id, m.media_type
		FROM messages m
		JOIN chats c ON m.chat_jid = c.jid
		WHERE datetime(m.timestamp) > datetime(?) AND datetime(m.timestamp) < datetime(?)
		AND m.is_from_me = 0
		AND m.content LIKE '%?'
		ORDER BY m.timestamp DESC
		LIMIT ?
	`

	rows, err := d.Messages.Query(query, after, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []domain.Message
	for rows.Next() {
		msg, err := scanMessage(rows)
		if err == nil {
			messages = append(messages, msg)
		}
	}

	return messages, nil
}

// GetMediaSummary counts media messages by type in a time range.
func (d *DB) GetMediaSummary(after, before string) (*domain.MediaSummary, error) {
	summary := &domain.MediaSummary{}

	query := `
		SELECT
			media_type,
			COUNT(*) as count
		FROM messages
		WHERE datetime(timestamp) > datetime(?) AND datetime(timestamp) < datetime(?)
		AND media_type IS NOT NULL
		GROUP BY media_type
	`

	rows, err := d.Messages.Query(query, after, before)
	if err != nil {
		return summary, err
	}
	defer rows.Close()

	for rows.Next() {
		var mediaType string
		var count int
		if err := rows.Scan(&mediaType, &count); err != nil {
			continue
		}

		switch {
		case strings.Contains(strings.ToLower(mediaType), "image"):
			summary.PhotoCount += count
		case strings.Contains(strings.ToLower(mediaType), "video"):
			summary.VideoCount += count
		case strings.Contains(strings.ToLower(mediaType), "audio"):
			summary.AudioCount += count
		case strings.Contains(strings.ToLower(mediaType), "document"):
			summary.DocumentCount += count
		}
	}

	// Get chats with media
	chatQuery := `
		SELECT DISTINCT c.name
		FROM messages m
		JOIN chats c ON m.chat_jid = c.jid
		WHERE datetime(m.timestamp) > datetime(?) AND datetime(m.timestamp) < datetime(?)
		AND m.media_type IS NOT NULL
		LIMIT 10
	`

	chatRows, err := d.Messages.Query(chatQuery, after, before)
	if err == nil {
		defer chatRows.Close()
		for chatRows.Next() {
			var chatName sql.NullString
			if err := chatRows.Scan(&chatName); err == nil && chatName.Valid {
				summary.FromChats = append(summary.FromChats, chatName.String)
			}
		}
	}

	return summary, nil
}
