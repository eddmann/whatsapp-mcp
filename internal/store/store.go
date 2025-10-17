package store

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
    Messages *sql.DB
}

func Open(dbDir string) (*DB, error) {
    if err := os.MkdirAll(dbDir, 0755); err != nil {
        return nil, fmt.Errorf("failed to create db dir: %w", err)
    }

    messagesPath := fmt.Sprintf("file:%s/messages.db?_foreign_keys=on", dbDir)
    mdb, err := sql.Open("sqlite3", messagesPath)
    if err != nil {
        return nil, fmt.Errorf("failed to open messages db: %w", err)
    }

    if err := migrate(mdb); err != nil {
        _ = mdb.Close()
        return nil, err
    }

    return &DB{Messages: mdb}, nil
}

func (d *DB) Close() error {
    if d == nil {
        return nil
    }
    if d.Messages != nil {
        return d.Messages.Close()
    }
    return nil
}

func migrate(db *sql.DB) error {
    _, err := db.Exec(`
        CREATE TABLE IF NOT EXISTS chats (
            jid TEXT PRIMARY KEY,
            name TEXT,
            last_message_time TIMESTAMP
        );

        CREATE TABLE IF NOT EXISTS messages (
            id TEXT,
            chat_jid TEXT,
            sender TEXT,
            content TEXT,
            timestamp TIMESTAMP,
            is_from_me BOOLEAN,
            media_type TEXT,
            filename TEXT,
            url TEXT,
            media_key BLOB,
            file_sha256 BLOB,
            file_enc_sha256 BLOB,
            file_length INTEGER,
            PRIMARY KEY (id, chat_jid),
            FOREIGN KEY (chat_jid) REFERENCES chats(jid)
        );

    `)
    if err != nil {
        return fmt.Errorf("failed to run migrations: %w", err)
    }
    // Enforce FTS5 availability and initialize virtual table and triggers
    if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
        content,
        content='messages',
        content_rowid='rowid'
    );`); err != nil {
        // Common error messages when FTS5 isn't compiled in: "no such module: fts5" or mentions of "fts5"
        if strings.Contains(strings.ToLower(err.Error()), "fts5") || strings.Contains(strings.ToLower(err.Error()), "no such module") {
            return fmt.Errorf("SQLite FTS5 is not available in the current build. Rebuild with CGO enabled and the go-sqlite3 'sqlite_fts5' build tag, e.g.: GO111MODULE=on CGO_ENABLED=1 go build -tags 'sqlite_fts5'. Under macOS, ensure Xcode CLT is installed.")
        }
        return err
    }
    if _, err := db.Exec(`CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
        INSERT INTO messages_fts(rowid, content)
        VALUES (new.rowid, new.content);
    END;`); err != nil {
        return err
    }
    if _, err := db.Exec(`CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
        INSERT INTO messages_fts(messages_fts, rowid) VALUES('delete', old.rowid);
    END;`); err != nil {
        return err
    }
    if _, err := db.Exec(`CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE ON messages BEGIN
        INSERT INTO messages_fts(messages_fts, rowid) VALUES('delete', old.rowid);
        INSERT INTO messages_fts(rowid, content)
        VALUES (new.rowid, new.content);
    END;`); err != nil {
        return err
    }
    // Ensure messages_fts exists now
    var tbl string
    if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='messages_fts'`).Scan(&tbl); err != nil {
        return fmt.Errorf("messages_fts not present after migration: %w", err)
    }
    // Rebuild the index to backfill from existing messages
    _, _ = db.Exec(`INSERT INTO messages_fts(messages_fts) VALUES('rebuild')`)
    return nil
}

func optionalExec(db *sql.DB, q string) error {
    if _, err := db.Exec(q); err != nil {
        if strings.Contains(err.Error(), "fts5") {
            return nil
        }
        return err
    }
    return nil
}


// MListChats returns chats with optional basic fields.
func (d *DB) MListChats(query string, limit, page int, sortBy string, includeLast bool) (any, error) {
    if limit <= 0 { limit = 20 }
    if page < 0 { page = 0 }
    orderBy := "chats.last_message_time DESC"
    if sortBy == "name" { orderBy = "chats.name" }

    var q string
    if includeLast {
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
    if query != "" {
        q += " WHERE (LOWER(chats.name) LIKE LOWER(?) OR chats.jid LIKE ?)"
        args = append(args, "%"+query+"%", "%"+query+"%")
    }
    q += fmt.Sprintf(" ORDER BY %s LIMIT ? OFFSET ?", orderBy)
    args = append(args, limit, page*limit)

    rows, err := d.Messages.Query(q, args...)
    if err != nil { return nil, err }
    defer rows.Close()

    type chat struct {
        JID string `json:"jid"`
        Name *string `json:"name"`
        LastMessageTime *string `json:"last_message_time"`
        LastMessage *string `json:"last_message"`
        LastSender *string `json:"last_sender"`
        LastIsFromMe *bool `json:"last_is_from_me"`
    }
    res := []chat{}
    for rows.Next() {
        var c chat
        var ts, name sql.NullString
        if includeLast {
            var lastMsg, lastSender sql.NullString
            var lastFromMe sql.NullBool
            if err := rows.Scan(&c.JID, &name, &ts, &lastMsg, &lastSender, &lastFromMe); err != nil { return nil, err }
            if lastMsg.Valid { c.LastMessage = &lastMsg.String }
            if lastSender.Valid { c.LastSender = &lastSender.String }
            if lastFromMe.Valid { c.LastIsFromMe = &lastFromMe.Bool }
        } else {
            if err := rows.Scan(&c.JID, &name, &ts); err != nil { return nil, err }
        }
        if name.Valid { c.Name = &name.String }
        if ts.Valid { c.LastMessageTime = &ts.String }
        res = append(res, c)
    }
    return map[string]any{"chats": res}, nil
}

func (d *DB) MSearchContacts(query string) (any, error) {
    pattern := "%" + strings.ToLower(query) + "%"
    rows, err := d.Messages.Query(`
        SELECT DISTINCT jid, name FROM chats
        WHERE (LOWER(name) LIKE ? OR LOWER(jid) LIKE ?) AND jid NOT LIKE '%@g.us'
        ORDER BY name, jid LIMIT 50`, pattern, pattern)
    if err != nil { return nil, err }
    defer rows.Close()
    type contact struct{ Phone string `json:"phone_number"`; Name *string `json:"name"`; JID string `json:"jid"` }
    var res []contact
    for rows.Next() {
        var jid string
        var name sql.NullString
        if err := rows.Scan(&jid, &name); err != nil { return nil, err }
        c := contact{JID: jid, Phone: strings.Split(jid, "@")[0]}
        if name.Valid { c.Name = &name.String }
        res = append(res, c)
    }
    return map[string]any{"contacts": res}, nil
}

func (d *DB) MGetChat(chatJID string, includeLast bool) (any, error) {
    row := d.Messages.QueryRow(`SELECT c.jid, c.name, c.last_message_time FROM chats c WHERE c.jid = ?`, chatJID)
    var jid string; var name, ts sql.NullString
    if err := row.Scan(&jid, &name, &ts); err != nil { return nil, err }
    m := map[string]any{"jid": jid}
    if name.Valid { m["name"] = name.String }
    if ts.Valid { m["last_message_time"] = ts.String }
    if includeLast {
        r := d.Messages.QueryRow(`SELECT content, sender, is_from_me FROM messages WHERE chat_jid = ? ORDER BY timestamp DESC LIMIT 1`, chatJID)
        var content sql.NullString; var sender sql.NullString; var isFromMe sql.NullBool
        _ = r.Scan(&content, &sender, &isFromMe)
        if content.Valid { m["last_message"] = content.String }
        if sender.Valid { m["last_sender"] = sender.String }
        if isFromMe.Valid { m["last_is_from_me"] = isFromMe.Bool }
    }
    return m, nil
}

func (d *DB) MListMessages(after, before, sender, chatJID, query string, limit, page int, includeContext bool, contextBefore, contextAfter int) (any, error) {
    parts := []string{"SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type FROM messages JOIN chats ON messages.chat_jid = chats.jid"}
    where := []string{}; args := []any{}
    if after != "" { where = append(where, "messages.timestamp > ?"); args = append(args, after) }
    if before != "" { where = append(where, "messages.timestamp < ?"); args = append(args, before) }
    if sender != "" { where = append(where, "messages.sender = ?"); args = append(args, sender) }
    if chatJID != "" { where = append(where, "messages.chat_jid = ?"); args = append(args, chatJID) }
    if query != "" { where = append(where, "LOWER(messages.content) LIKE LOWER(?)"); args = append(args, "%"+query+"%") }
    if len(where) > 0 { parts = append(parts, "WHERE "+strings.Join(where, " AND ")) }
    if limit <= 0 { limit = 20 }
    if page < 0 { page = 0 }
    parts = append(parts, "ORDER BY messages.timestamp DESC", "LIMIT ? OFFSET ?")
    args = append(args, limit, page*limit)
    rows, err := d.Messages.Query(strings.Join(parts, " "), args...)
    if err != nil { return nil, err }
    defer rows.Close()
    type msg struct { Timestamp string `json:"timestamp"`; Sender string `json:"sender"`; ChatName *string `json:"chat_name"`; Content *string `json:"content"`; IsFromMe bool `json:"is_from_me"`; ChatJID string `json:"chat_jid"`; ID string `json:"id"`; MediaType *string `json:"media_type"` }
    var res []msg
    for rows.Next() {
        var m msg; var chatName, content, media sql.NullString; var isFromMe bool; var ts string
        if err := rows.Scan(&ts, &m.Sender, &chatName, &content, &isFromMe, &m.ChatJID, &m.ID, &media); err != nil { return nil, err }
        m.Timestamp = ts; m.IsFromMe = isFromMe
        if chatName.Valid { m.ChatName = &chatName.String }
        if content.Valid { m.Content = &content.String }
        if media.Valid { m.MediaType = &media.String }
        res = append(res, m)
    }
    // If includeContext, expand with before/after messages per match
    if includeContext && len(res) > 0 {
        out := make([]msg, 0, len(res)*(2+contextBefore+contextAfter))
        for _, base := range res {
            // current message
            out = append(out, base)
            // fetch before
            if contextBefore > 0 {
                rowsB, err := d.Messages.Query(`SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type FROM messages JOIN chats ON messages.chat_jid = chats.jid WHERE messages.chat_jid = ? AND messages.timestamp < ? ORDER BY messages.timestamp DESC LIMIT ?`, base.ChatJID, base.Timestamp, contextBefore)
                if err == nil {
                    for rowsB.Next() {
                        var m msg; var chatName, content, media sql.NullString; var isFromMe bool; var ts string
                        if err := rowsB.Scan(&ts, &m.Sender, &chatName, &content, &isFromMe, &m.ChatJID, &m.ID, &media); err == nil {
                            m.Timestamp = ts; m.IsFromMe = isFromMe
                            if chatName.Valid { m.ChatName = &chatName.String }
                            if content.Valid { m.Content = &content.String }
                            if media.Valid { m.MediaType = &media.String }
                            out = append(out, m)
                        }
                    }
                    rowsB.Close()
                }
            }
            // fetch after
            if contextAfter > 0 {
                rowsA, err := d.Messages.Query(`SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type FROM messages JOIN chats ON messages.chat_jid = chats.jid WHERE messages.chat_jid = ? AND messages.timestamp > ? ORDER BY messages.timestamp ASC LIMIT ?`, base.ChatJID, base.Timestamp, contextAfter)
                if err == nil {
                    for rowsA.Next() {
                        var m msg; var chatName, content, media sql.NullString; var isFromMe bool; var ts string
                        if err := rowsA.Scan(&ts, &m.Sender, &chatName, &content, &isFromMe, &m.ChatJID, &m.ID, &media); err == nil {
                            m.Timestamp = ts; m.IsFromMe = isFromMe
                            if chatName.Valid { m.ChatName = &chatName.String }
                            if content.Valid { m.Content = &content.String }
                            if media.Valid { m.MediaType = &media.String }
                            out = append(out, m)
                        }
                    }
                    rowsA.Close()
                }
            }
        }
        res = out
    }
    return map[string]any{"messages": res}, nil
}

func (d *DB) MGetMessageContext(messageID string, before, after int) (any, error) {
    if before <= 0 { before = 5 }; if after <= 0 { after = 5 }
    row := d.Messages.QueryRow(`SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.chat_jid, messages.media_type FROM messages JOIN chats ON messages.chat_jid = chats.jid WHERE messages.id = ?`, messageID)
    var ts, sender, chatJ string; var id string; var chatName, content, media sql.NullString; var isFromMe bool
    if err := row.Scan(&ts, &sender, &chatName, &content, &isFromMe, &chatJ, &id, &chatJ, &media); err != nil { return nil, err }
    mk := func(rows *sql.Rows) ([]map[string]any, error) {
        var out []map[string]any
        for rows.Next() {
            var ts2 string; var sender2, chatJ2, id2 string; var chatName2, content2, media2 sql.NullString; var isFromMe2 bool
            if err := rows.Scan(&ts2, &sender2, &chatName2, &content2, &isFromMe2, &chatJ2, &id2, &media2); err != nil { return nil, err }
            m := map[string]any{"timestamp": ts2, "sender": sender2, "is_from_me": isFromMe2, "chat_jid": chatJ2, "id": id2}
            if chatName2.Valid { m["chat_name"] = chatName2.String }
            if content2.Valid { m["content"] = content2.String }
            if media2.Valid { m["media_type"] = media2.String }
            out = append(out, m)
        }
        return out, nil
    }
    beforeRows, err := d.Messages.Query(`SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type FROM messages JOIN chats ON messages.chat_jid = chats.jid WHERE messages.chat_jid = ? AND messages.timestamp < ? ORDER BY messages.timestamp DESC LIMIT ?`, chatJ, ts, before)
    if err != nil { return nil, err }
    defer beforeRows.Close()
    afterRows, err := d.Messages.Query(`SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type FROM messages JOIN chats ON messages.chat_jid = chats.jid WHERE messages.chat_jid = ? AND messages.timestamp > ? ORDER BY messages.timestamp ASC LIMIT ?`, chatJ, ts, after)
    if err != nil { return nil, err }
    defer afterRows.Close()
    beforeList, err := mk(beforeRows); if err != nil { return nil, err }
    afterList, err := mk(afterRows); if err != nil { return nil, err }
    current := map[string]any{"timestamp": ts, "sender": sender, "is_from_me": isFromMe, "chat_jid": chatJ, "id": id}
    if chatName.Valid { current["chat_name"] = chatName.String }
    if content.Valid { current["content"] = content.String }
    if media.Valid { current["media_type"] = media.String }
    return map[string]any{"message": current, "before": beforeList, "after": afterList}, nil
}

func (d *DB) MGetDirectChatByContact(phone string) (any, error) {
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
    var jid string; var name, ts sql.NullString
    var lastMsg, lastSender sql.NullString; var lastFromMe sql.NullBool
    if err := row.Scan(&jid, &name, &ts, &lastMsg, &lastSender, &lastFromMe); err != nil { return nil, err }
    m := map[string]any{"jid": jid}
    if name.Valid { m["name"] = name.String }
    if ts.Valid { m["last_message_time"] = ts.String }
    if lastMsg.Valid { m["last_message"] = lastMsg.String }
    if lastSender.Valid { m["last_sender"] = lastSender.String }
    if lastFromMe.Valid { m["last_is_from_me"] = lastFromMe.Bool }
    return m, nil
}

func (d *DB) MGetContactChats(jid string, limit, page int) (any, error) {
    if limit <= 0 { limit = 20 }; if page < 0 { page = 0 }
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
    if err != nil { return nil, err }
    defer rows.Close()
    type chat struct { JID string `json:"jid"`; Name *string `json:"name"`; LastMessageTime *string `json:"last_message_time"`; LastMessage *string `json:"last_message"`; LastSender *string `json:"last_sender"`; LastIsFromMe *bool `json:"last_is_from_me"` }
    var res []chat
    for rows.Next() {
        var c chat; var name, ts sql.NullString
        var lastMsg, lastSender sql.NullString; var lastFromMe sql.NullBool
        if err := rows.Scan(&c.JID, &name, &ts, &lastMsg, &lastSender, &lastFromMe); err != nil { return nil, err }
        if name.Valid { c.Name = &name.String }
        if ts.Valid { c.LastMessageTime = &ts.String }
        if lastMsg.Valid { c.LastMessage = &lastMsg.String }
        if lastSender.Valid { c.LastSender = &lastSender.String }
        if lastFromMe.Valid { c.LastIsFromMe = &lastFromMe.Bool }
        res = append(res, c)
    }
    return map[string]any{"chats": res}, nil
}

func (d *DB) MGetLastInteraction(jid string) (any, error) {
    row := d.Messages.QueryRow(`
        SELECT m.timestamp, m.sender, c.name, m.content, m.is_from_me, c.jid, m.id, m.media_type
        FROM messages m JOIN chats c ON m.chat_jid = c.jid
        WHERE m.sender = ? OR c.jid = ?
        ORDER BY m.timestamp DESC LIMIT 1`, jid, jid)
    var ts, sender, chatJID, id string; var name, content, media sql.NullString; var isFromMe bool
    if err := row.Scan(&ts, &sender, &name, &content, &isFromMe, &chatJID, &id, &media); err != nil { return nil, err }
    m := map[string]any{
        "timestamp": ts,
        "sender": sender,
        "is_from_me": isFromMe,
        "chat_jid": chatJID,
        "id": id,
    }
    if name.Valid && name.String != "" { m["chat_name"] = name.String }
    if content.Valid { m["content"] = content.String }
    if media.Valid && media.String != "" { m["media_type"] = media.String }
    return map[string]any{"message": m}, nil
}

func (d *DB) MSearchMessages(query string, limit, page int) (any, error) {
    if limit <= 0 { limit = 20 }; if page < 0 { page = 0 }
    // Try FTS5
    rows, err := d.Messages.Query(`
        SELECT m.timestamp, m.sender, c.name, m.content, m.is_from_me, m.chat_jid, m.id, m.media_type
        FROM messages_fts f
        JOIN messages m ON m.rowid = f.rowid
        JOIN chats c ON m.chat_jid = c.jid
        WHERE messages_fts MATCH ?
        ORDER BY m.timestamp DESC
        LIMIT ? OFFSET ?`, query, limit, page*limit)
    if err != nil {
        // fallback LIKE
        rows, err = d.Messages.Query(`
            SELECT m.timestamp, m.sender, c.name, m.content, m.is_from_me, m.chat_jid, m.id, m.media_type
            FROM messages m JOIN chats c ON m.chat_jid = c.jid
            WHERE LOWER(m.content) LIKE LOWER(?)
            ORDER BY m.timestamp DESC
            LIMIT ? OFFSET ?`, "%"+query+"%", limit, page*limit)
        if err != nil { return nil, err }
    }
    defer rows.Close()
    type msg struct { Timestamp string `json:"timestamp"`; Sender string `json:"sender"`; ChatName *string `json:"chat_name"`; Content *string `json:"content"`; IsFromMe bool `json:"is_from_me"`; ChatJID string `json:"chat_jid"`; ID string `json:"id"`; MediaType *string `json:"media_type"` }
    var res []msg
    for rows.Next() {
        var m msg; var chatName, content, media sql.NullString
        if err := rows.Scan(&m.Timestamp, &m.Sender, &chatName, &content, &m.IsFromMe, &m.ChatJID, &m.ID, &media); err != nil { return nil, err }
        if chatName.Valid { m.ChatName = &chatName.String }
        if content.Valid { m.Content = &content.String }
        if media.Valid { m.MediaType = &media.String }
        res = append(res, m)
    }
    return map[string]any{"messages": res}, nil
}

// formatMessageLine produces a single-line human-readable message similar to the reference.
func (d *DB) formatMessageLine(ts string, chatName *string, sender string, content *string, isFromMe bool, chatJID string, id string, mediaType *string) string {
    var b strings.Builder
    // Parse timestamp for formatting safety
    t := ts
    if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
        t = parsed.Format("2006-01-02 15:04:05")
    }
    if chatName != nil && *chatName != "" {
        b.WriteString("["); b.WriteString(t); b.WriteString("] Chat: "); b.WriteString(*chatName); b.WriteString(" ")
    } else {
        b.WriteString("["); b.WriteString(t); b.WriteString("] ")
    }
    // Determine sender display
    senderDisplay := "Me"
    if !isFromMe {
        if n := d.lookupSenderName(sender); n != "" {
            senderDisplay = n
        } else {
            senderDisplay = sender
        }
    }
    b.WriteString("From: "); b.WriteString(senderDisplay); b.WriteString(": ")
    if mediaType != nil && *mediaType != "" {
        b.WriteString("["); b.WriteString(*mediaType); b.WriteString(" - Message ID: "); b.WriteString(id); b.WriteString(" - Chat JID: "); b.WriteString(chatJID); b.WriteString("] ")
    }
    if content != nil { b.WriteString(*content) }
    b.WriteString("\n")
    return b.String()
}

// lookupSenderName attempts to resolve a sender JID or number to a saved chat name.
func (d *DB) lookupSenderName(sender string) string {
    // Try exact JID
    var name sql.NullString
    if err := d.Messages.QueryRow(`SELECT name FROM chats WHERE jid = ? LIMIT 1`, sender).Scan(&name); err == nil {
        if name.Valid && name.String != "" { return name.String }
    }
    // Try by phone substring from JID
    phone := sender
    if i := strings.Index(sender, "@"); i > 0 { phone = sender[:i] }
    if err := d.Messages.QueryRow(`SELECT name FROM chats WHERE jid LIKE ? LIMIT 1`, "%"+phone+"%").Scan(&name); err == nil {
        if name.Valid && name.String != "" { return name.String }
    }
    return ""
}


