package wa

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/mdp/qrterminal"
	"go.mau.fi/whatsmeow"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/eddmann/whatsapp-mcp/internal/media"
	"github.com/eddmann/whatsapp-mcp/internal/store"
)

type Client struct {
    WA      *whatsmeow.Client
    Store   *store.DB
    Logger  *slog.Logger
    BaseDir string
}

func New(db *store.DB, baseDir string, logLevel string, appLogger *slog.Logger) (*Client, error) {
    if baseDir == "" {
        baseDir = "store"
    }
    if logLevel == "" {
        logLevel = "INFO"
    }
    // Normalize log level
    lvl := strings.ToUpper(logLevel)
    switch lvl {
    case "DEBUG", "INFO", "WARN", "ERROR":
        // ok
    default:
        lvl = "INFO"
    }
    waLogger := waLog.Stdout("wa", lvl, true)
    dbLog := waLog.Stdout("wa-db", lvl, true)
    if appLogger == nil {
        appLogger = slog.Default()
    }

    if err := os.MkdirAll(baseDir, 0755); err != nil {
        return nil, fmt.Errorf("failed to create store dir: %w", err)
    }

    waDBURI := fmt.Sprintf("file:%s/whatsapp.db?_foreign_keys=on", baseDir)
    container, err := sqlstore.New(context.Background(), "sqlite3", waDBURI, dbLog)
    if err != nil {
        return nil, fmt.Errorf("failed to open wa session db: %w", err)
    }

    deviceStore, err := container.GetFirstDevice(context.Background())
    if err != nil {
        if err == sql.ErrNoRows {
            deviceStore = container.NewDevice()
        } else {
            return nil, fmt.Errorf("failed to get device: %w", err)
        }
    }

    client := whatsmeow.NewClient(deviceStore, waLogger)
    if client == nil {
        return nil, fmt.Errorf("failed to create client")
    }

    c := &Client{WA: client, Store: db, Logger: appLogger, BaseDir: baseDir}
    c.registerHandlers()
    return c, nil
}

func (c *Client) registerHandlers() {
    c.WA.AddEventHandler(func(evt interface{}) {
        switch v := evt.(type) {
        case *events.Message:
            c.handleMessage(v)
        case *events.HistorySync:
            c.handleHistorySync(v)
        case *events.Connected:
            c.Logger.Info("connected")
            // After connecting, backfill chat names from contacts/groups
            go c.backfillChatNames()
        case *events.LoggedOut:
            c.Logger.Warn("logged out")
        }
    })
}

func (c *Client) ConnectWithQR(ctx context.Context) error {
    if c.WA.Store.ID == nil {
        qrChan, _ := c.WA.GetQRChannel(ctx)
        if err := c.WA.Connect(); err != nil {
            return err
        }
        for evt := range qrChan {
            if evt.Event == "code" {
                qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
            } else if evt.Event == "success" {
                break
            }
        }
        return nil
    }
    return c.WA.Connect()
}

func (c *Client) handleMessage(msg *events.Message) {
    chatJID := msg.Info.Chat.String()
    sender := msg.Info.Sender.User
    content := extractTextContent(msg.Message)
    mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength := extractMediaInfo(msg.Message)
    if content == "" && mediaType == "" {
        return
    }
    // Ensure we have a per-sender chat entry with a friendly name for name lookups
    if sender != "" {
        indiv := types.JID{User: sender, Server: "s.whatsapp.net"}
        var existing sql.NullString
        _ = c.Store.Messages.QueryRow("SELECT name FROM chats WHERE jid = ?", indiv.String()).Scan(&existing)
        if !existing.Valid {
            resolved := c.resolvePreferredName(indiv)
            _, _ = c.Store.Messages.Exec("INSERT INTO chats (jid, name) VALUES (?, ?)", indiv.String(), resolved)
        } else if existing.String == "" {
            resolved := c.resolvePreferredName(indiv)
            if resolved != "" {
                _, _ = c.Store.Messages.Exec("UPDATE chats SET name = ? WHERE jid = ?", resolved, indiv.String())
            }
        }
    }
    // Determine and persist chat name
    name := c.getChatName(msg.Info.Chat, chatJID, nil, sender)
    if _, err := c.Store.Messages.Exec("INSERT OR REPLACE INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)", chatJID, name, msg.Info.Timestamp); err != nil {
        c.Logger.Warn("failed to upsert chat", "jid", chatJID, "err", err)
    }
    // Insert message
    if _, err := c.Store.Messages.Exec(`INSERT OR REPLACE INTO messages 
        (id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        msg.Info.ID, chatJID, sender, content, msg.Info.Timestamp, msg.Info.IsFromMe, mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength,
    ); err != nil {
        c.Logger.Warn("failed to store message", "id", msg.Info.ID, "chat_jid", chatJID, "err", err)
    }
}

// handleHistorySync persists conversations and messages received during a history sync.
func (c *Client) handleHistorySync(hs *events.HistorySync) {
    if hs == nil || hs.Data.Conversations == nil {
        return
    }
    synced := 0
    for _, conv := range hs.Data.Conversations {
        if conv == nil || conv.ID == nil {
            continue
        }
        chatJID := *conv.ID
        jid, err := types.ParseJID(chatJID)
        if err != nil {
            c.Logger.Warn("history sync: bad JID", "jid", chatJID, "err", err)
            continue
        }
        // Try to extract name from conversation; fallback to contacts/groups
        name := c.getChatName(jid, chatJID, conv, "")
        // Update chat last_message_time using latest message
        if len(conv.Messages) > 0 && conv.Messages[0] != nil && conv.Messages[0].Message != nil {
            ts := conv.Messages[0].Message.GetMessageTimestamp()
            if ts != 0 {
                t := time.Unix(int64(ts), 0)
                if _, err := c.Store.Messages.Exec("INSERT OR REPLACE INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)", chatJID, name, t); err != nil {
                    c.Logger.Warn("history sync: failed to upsert chat", "jid", chatJID, "err", err)
                }
            }
        }
        
        // Store each message
        for _, m := range conv.Messages {
            if m == nil || m.Message == nil {
                continue
            }
            // Text and media
            var text string
            if m.Message.Message != nil {
                text = extractTextContent(m.Message.Message)
            }
            mt, fn, u, mk, sha, enc, fl := "", "", "", ([]byte)(nil), ([]byte)(nil), ([]byte)(nil), uint64(0)
            if m.Message.Message != nil {
                mt, fn, u, mk, sha, enc, fl = extractMediaInfo(m.Message.Message)
            }
            if text == "" && mt == "" { 
                c.Logger.Debug("history sync: skipping non-text/non-media message", "key", m.Message.Key)
                continue
            }
            // Sender and fromMe
            fromMe := false
            snd := jid.User
            if m.Message.Key != nil {
                if m.Message.Key.FromMe != nil { fromMe = *m.Message.Key.FromMe }
                if !fromMe && m.Message.Key.Participant != nil && *m.Message.Key.Participant != "" { snd = *m.Message.Key.Participant }
                if fromMe && c.WA != nil && c.WA.Store != nil && c.WA.Store.ID != nil { snd = c.WA.Store.ID.User }
            }
            // Normalize sender to phone user part if it looks like a JID (e.g. number@lid)
            if strings.Contains(snd, "@") {
                if pj, err := types.ParseJID(snd); err == nil {
                    snd = pj.User
                } else {
                    if i := strings.Index(snd, "@"); i > 0 { snd = snd[:i] }
                }
            }
            // Upsert a per-sender chat entry for name resolution
            if !fromMe && snd != "" {
                indiv := types.JID{User: snd, Server: "s.whatsapp.net"}
                var existing sql.NullString
                _ = c.Store.Messages.QueryRow("SELECT name FROM chats WHERE jid = ?", indiv.String()).Scan(&existing)
                if !existing.Valid {
                    resolved := c.resolvePreferredName(indiv)
                    _, _ = c.Store.Messages.Exec("INSERT INTO chats (jid, name) VALUES (?, ?)", indiv.String(), resolved)
                } else if existing.String == "" {
                    resolved := c.resolvePreferredName(indiv)
                    if resolved != "" {
                        _, _ = c.Store.Messages.Exec("UPDATE chats SET name = ? WHERE jid = ?", resolved, indiv.String())
                    }
                }
            }
            // IDs and timestamp
            id := ""
            if m.Message.Key != nil && m.Message.Key.ID != nil { id = *m.Message.Key.ID }
            ts := m.Message.GetMessageTimestamp()
            if ts == 0 { continue }
            t := time.Unix(int64(ts), 0)
            // Persist
            if _, err := c.Store.Messages.Exec(`INSERT OR REPLACE INTO messages 
                (id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, chatJID, snd, text, t, fromMe, mt, fn, u, mk, sha, enc, fl); err != nil {
                c.Logger.Warn("history sync: failed to store message", "id", id, "chat_jid", chatJID, "err", err)
                continue
            }
            synced++
        }
    }
    c.Logger.Info("history sync persisted messages", "count", synced)
}

// getChatName attempts to resolve a friendly chat name using existing DB, conversation metadata, group info, or contacts.
func (c *Client) getChatName(jid types.JID, chatJID string, conversation any, sender string) string {
    // Existing stored name
    var existing sql.NullString
    _ = c.Store.Messages.QueryRow("SELECT name FROM chats WHERE jid = ?", chatJID).Scan(&existing)
    if existing.Valid && existing.String != "" { return existing.String }

    // Try to extract from conversation (DisplayName or Name), using reflection to avoid tight coupling
    if conversation != nil {
        v := reflect.ValueOf(conversation)
        if v.Kind() == reflect.Ptr && !v.IsNil() { v = v.Elem() }
        if v.IsValid() {
            if f := v.FieldByName("DisplayName"); f.IsValid() && f.Kind() == reflect.Ptr && !f.IsNil() {
                if dn, ok := f.Elem().Interface().(string); ok && dn != "" { return dn }
            }
            if f := v.FieldByName("Name"); f.IsValid() && f.Kind() == reflect.Ptr && !f.IsNil() {
                if n, ok := f.Elem().Interface().(string); ok && n != "" { return n }
            }
        }
    }

    // Groups
    if jid.Server == "g.us" {
        if info, err := c.WA.GetGroupInfo(jid); err == nil && info.Name != "" {
            return info.Name
        }
        return fmt.Sprintf("Group %s", jid.User)
    }

    // Contacts
	if contact, err := c.WA.Store.Contacts.GetContact(context.Background(), jid); err == nil {
		if contact.FullName != "" {
			return contact.FullName
		}
		if contact.BusinessName != "" {
			return contact.BusinessName
		}
		if contact.PushName != "" {
			return contact.PushName
		}
	}
    if sender != "" { return sender }
    return jid.User
}

// resolvePreferredName tries to resolve a human-friendly name for a JID using
// live WA data only (contacts/groups), ignoring any cached DB value. This is
// used by backfill to improve chats that only have phone numbers stored.
func (c *Client) resolvePreferredName(jid types.JID) string {
    // Groups
    if jid.Server == "g.us" {
        if info, err := c.WA.GetGroupInfo(jid); err == nil && info.Name != "" {
            return info.Name
        }
        return fmt.Sprintf("Group %s", jid.User)
    }
    // Contacts
    if contact, err := c.WA.Store.Contacts.GetContact(context.Background(), jid); err == nil {
        if contact.FullName != "" { return contact.FullName }
        if contact.BusinessName != "" { return contact.BusinessName }
        if contact.PushName != "" { return contact.PushName }
    }
    return jid.User
}

// backfillChatNames finds chats without a proper name and updates them using
// contact/group information once available post-connect.
func (c *Client) backfillChatNames() {
    if c.Store == nil || c.Store.Messages == nil {
        return
    }
    rows, err := c.Store.Messages.Query(`SELECT jid, COALESCE(name, '') FROM chats`)
    if err != nil {
        c.Logger.Warn("backfill: query chats failed", "err", err)
        return
    }
    defer rows.Close()
    type row struct{ jid string; name string }
    var toUpdate []row
    for rows.Next() {
        var jidStr, name string
        if err := rows.Scan(&jidStr, &name); err != nil {
            c.Logger.Warn("backfill: scan failed", "err", err)
            continue
        }
        // Skip groups here; resolvePreferredName handles them but they usually already have names
        parsed, err := types.ParseJID(jidStr)
        if err != nil { continue }
        if parsed.Server == "g.us" {
            // update groups if missing a name
            if name == "" || name == parsed.User {
                toUpdate = append(toUpdate, row{jid: jidStr, name: name})
            }
            continue
        }
        // For individual chats: consider missing or numeric-like names as needing backfill
        phone := parsed.User
        if name == "" || name == phone || strings.HasSuffix(name, "@s.whatsapp.net") {
            toUpdate = append(toUpdate, row{jid: jidStr, name: name})
        }
    }
    if err := rows.Err(); err != nil {
        c.Logger.Warn("backfill: rows error", "err", err)
    }

    updated := 0
    for _, r := range toUpdate {
        parsed, err := types.ParseJID(r.jid)
        if err != nil { continue }
        resolved := c.resolvePreferredName(parsed)
        if resolved == "" || resolved == parsed.User || resolved == r.name {
            continue
        }
        if _, err := c.Store.Messages.Exec(`UPDATE chats SET name = ? WHERE jid = ?`, resolved, r.jid); err != nil {
            c.Logger.Warn("backfill: update failed", "jid", r.jid, "err", err)
            continue
        }
        updated++
    }
    if updated > 0 {
        c.Logger.Info("backfill: updated chat names", "count", updated)
    }
}

func extractTextContent(m *waE2E.Message) string {
    if m == nil {
        return ""
    }

    // Basic text messages
    if t := m.GetConversation(); t != "" {
        return t
    }
    
    if et := m.GetExtendedTextMessage(); et != nil {
        return et.GetText()
    }
    
    // Location messages
    if loc := m.GetLocationMessage(); loc != nil {
        return fmt.Sprintf("📍 Location: %.6f, %.6f", loc.GetDegreesLatitude(), loc.GetDegreesLongitude())
    }
    
    // Contact messages
    if contact := m.GetContactMessage(); contact != nil {
        name := contact.GetDisplayName()
        if name == "" {
            name = "Contact"
        }
        return fmt.Sprintf("👤 %s", name)
    }
    
    // Sticker messages
    if sticker := m.GetStickerMessage(); sticker != nil {
        return "🎭 Sticker"
    }
    
    // Live location messages
    if liveLoc := m.GetLiveLocationMessage(); liveLoc != nil {
        return fmt.Sprintf("📍 Live Location: %.6f, %.6f", liveLoc.GetDegreesLatitude(), liveLoc.GetDegreesLongitude())
    }
    
    // Poll messages
    if poll := m.GetPollCreationMessage(); poll != nil {
        return fmt.Sprintf("📊 Poll: %s", poll.GetName())
    }
    
    // Reaction messages
    if reaction := m.GetReactionMessage(); reaction != nil {
        return fmt.Sprintf("😊 Reaction: %s", reaction.GetText())
    }
    
    // System messages and other types
    if m.GetProtocolMessage() != nil {
        return "🔧 System Message"
    }
    
    return ""
}

func extractMediaInfo(m *waE2E.Message) (mediaType, filename, url string, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64) {
    if m == nil { return "", "", "", nil, nil, nil, 0 }
    if img := m.GetImageMessage(); img != nil {
        return "image", fmt.Sprintf("image_%s.jpg", time.Now().Format("20060102_150405")), img.GetURL(), img.GetMediaKey(), img.GetFileSHA256(), img.GetFileEncSHA256(), img.GetFileLength()
    }
    if vid := m.GetVideoMessage(); vid != nil {
        return "video", fmt.Sprintf("video_%s.mp4", time.Now().Format("20060102_150405")), vid.GetURL(), vid.GetMediaKey(), vid.GetFileSHA256(), vid.GetFileEncSHA256(), vid.GetFileLength()
    }
    if aud := m.GetAudioMessage(); aud != nil {
        return "audio", fmt.Sprintf("audio_%s.ogg", time.Now().Format("20060102_150405")), aud.GetURL(), aud.GetMediaKey(), aud.GetFileSHA256(), aud.GetFileEncSHA256(), aud.GetFileLength()
    }
    if doc := m.GetDocumentMessage(); doc != nil {
        name := doc.GetFileName()
        if name == "" { name = fmt.Sprintf("document_%s", time.Now().Format("20060102_150405")) }
        return "document", name, doc.GetURL(), doc.GetMediaKey(), doc.GetFileSHA256(), doc.GetFileEncSHA256(), doc.GetFileLength()
    }
    if sticker := m.GetStickerMessage(); sticker != nil {
        return "sticker", fmt.Sprintf("sticker_%s.webp", time.Now().Format("20060102_150405")), sticker.GetURL(), sticker.GetMediaKey(), sticker.GetFileSHA256(), sticker.GetFileEncSHA256(), sticker.GetFileLength()
    }
    return "", "", "", nil, nil, nil, 0
}

// SendText sends a text message to a JID or phone number string (without +) or group JID.
func (c *Client) SendText(recipient, text string) (bool, string, error) {
    if !c.WA.IsConnected() { return false, "not connected", fmt.Errorf("not connected") }
    jid, err := parseRecipient(recipient)
    if err != nil { return false, "invalid recipient", err }
    msg := &waE2E.Message{ Conversation: protoString(text) }
    _, err = c.WA.SendMessage(context.Background(), jid, msg)
    if err != nil { return false, err.Error(), err }
    return true, fmt.Sprintf("sent to %s", recipient), nil
}

// SendMedia sends an image/video/document/audio; audio is PTT if .ogg.
func (c *Client) SendMedia(recipient, path string) (bool, string, error) {
    if !c.WA.IsConnected() { return false, "not connected", fmt.Errorf("not connected") }
    jid, err := parseRecipient(recipient)
    if err != nil { return false, "invalid recipient", err }
    b, err := os.ReadFile(path)
    if err != nil { return false, "read error", err }
    mediaType, mime := classify(path)
    up, err := c.WA.Upload(context.Background(), b, mediaType)
    if err != nil { return false, "upload failed", err }
    m := &waE2E.Message{}
    base := filepath.Base(path)
    switch mediaType {
    case whatsmeow.MediaImage:
        m.ImageMessage = &waE2E.ImageMessage{Caption: protoString(""), Mimetype: protoString(mime), URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: &up.FileLength}
    case whatsmeow.MediaVideo:
        m.VideoMessage = &waE2E.VideoMessage{Caption: protoString(""), Mimetype: protoString(mime), URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: &up.FileLength}
    case whatsmeow.MediaDocument:
        m.DocumentMessage = &waE2E.DocumentMessage{Title: protoString(base), Caption: protoString(""), Mimetype: protoString(mime), URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: &up.FileLength}
    case whatsmeow.MediaAudio:
        // If not .ogg, convert via ffmpeg
        if !isOgg(path) {
            cpath, err := media.ConvertToOpusOgg(path)
            if err != nil { return false, "conversion failed", err }
            defer func() { _ = os.Remove(cpath) }()
            b2, err := os.ReadFile(cpath)
            if err != nil { return false, "read converted", err }
            up2, err := c.WA.Upload(context.Background(), b2, whatsmeow.MediaAudio)
            if err != nil { return false, "upload converted", err }
            dur, waveform, _ := media.AnalyzeOggOpus(b2)
            m.AudioMessage = &waE2E.AudioMessage{Mimetype: protoString("audio/ogg; codecs=opus"), URL: &up2.URL, DirectPath: &up2.DirectPath, MediaKey: up2.MediaKey, FileEncSHA256: up2.FileEncSHA256, FileSHA256: up2.FileSHA256, FileLength: &up2.FileLength, Seconds: protoUint32(uint32(dur)), PTT: protoBool(true), Waveform: waveform}
        } else {
            dur, waveform, _ := media.AnalyzeOggOpus(b)
            m.AudioMessage = &waE2E.AudioMessage{Mimetype: protoString(mime), URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: &up.FileLength, Seconds: protoUint32(uint32(dur)), PTT: protoBool(true), Waveform: waveform}
        }
    }
    _, err = c.WA.SendMessage(context.Background(), jid, m)
    if err != nil { return false, err.Error(), err }
    return true, fmt.Sprintf("sent media to %s", recipient), nil
}

// DownloadMedia looks up media from DB and downloads via whatsmeow.
func (c *Client) DownloadMedia(messageID, chatJID string) (bool, string, string, string, error) {
    var mediaType, filename, url string
    var mediaKey, fileSHA256, fileEncSHA256 []byte
    var fileLength uint64
    row := c.Store.Messages.QueryRow("SELECT media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length FROM messages WHERE id = ? AND chat_jid = ?", messageID, chatJID)
    if err := row.Scan(&mediaType, &filename, &url, &mediaKey, &fileSHA256, &fileEncSHA256, &fileLength); err != nil {
        return false, "", "", "", err
    }
    if mediaType == "" || url == "" || len(mediaKey) == 0 || len(fileSHA256) == 0 || len(fileEncSHA256) == 0 || fileLength == 0 { return false, "", "", "", fmt.Errorf("incomplete media info") }
    dp := extractDirectPathFromURL(url)
    dm := &downloadable{URL: url, DirectPath: dp, MediaKey: mediaKey, FileLength: fileLength, FileSHA256: fileSHA256, FileEncSHA256: fileEncSHA256, MediaType: classifyToWA(mediaType)}
    data, err := c.WA.Download(context.Background(), dm)
    if err != nil { return false, "", "", "", err }
    outDir := filepath.Join(c.BaseDir, strings.ReplaceAll(chatJID, ":", "_"))
    if err := os.MkdirAll(outDir, 0755); err != nil { return false, "", "", "", err }
    out := filepath.Join(outDir, filename)
    if err := os.WriteFile(out, data, fs.FileMode(0644)); err != nil { return false, "", "", "", err }
    abs, _ := filepath.Abs(out)
    return true, mediaType, filename, abs, nil
}

func protoString(s string) *string { return &s }
func protoBool(b bool) *bool { return &b }
func protoUint32(u uint32) *uint32 { return &u }

func parseRecipient(recipient string) (types.JID, error) {
    if strings.Contains(recipient, "@") {
        return types.ParseJID(recipient)
    }
    return types.JID{User: recipient, Server: "s.whatsapp.net"}, nil
}

func classify(path string) (whatsmeow.MediaType, string) {
    ext := strings.ToLower(filepath.Ext(path))
    switch ext {
    case ".jpg", ".jpeg": return whatsmeow.MediaImage, "image/jpeg"
    case ".png": return whatsmeow.MediaImage, "image/png"
    case ".gif": return whatsmeow.MediaImage, "image/gif"
    case ".webp": return whatsmeow.MediaImage, "image/webp"
    case ".mp4": return whatsmeow.MediaVideo, "video/mp4"
    case ".avi": return whatsmeow.MediaVideo, "video/avi"
    case ".mov": return whatsmeow.MediaVideo, "video/quicktime"
    case ".ogg": return whatsmeow.MediaAudio, "audio/ogg; codecs=opus"
    default: return whatsmeow.MediaDocument, "application/octet-stream"
    }
}

func isOgg(path string) bool { return strings.ToLower(filepath.Ext(path)) == ".ogg" }

type downloadable struct {
    URL           string
    DirectPath    string
    MediaKey      []byte
    FileLength    uint64
    FileSHA256    []byte
    FileEncSHA256 []byte
    MediaType     whatsmeow.MediaType
}

func (d *downloadable) GetDirectPath() string    { return d.DirectPath }
func (d *downloadable) GetURL() string           { return d.URL }
func (d *downloadable) GetMediaKey() []byte      { return d.MediaKey }
func (d *downloadable) GetFileLength() uint64    { return d.FileLength }
func (d *downloadable) GetFileSHA256() []byte    { return d.FileSHA256 }
func (d *downloadable) GetFileEncSHA256() []byte { return d.FileEncSHA256 }
func (d *downloadable) GetMediaType() whatsmeow.MediaType { return d.MediaType }

func extractDirectPathFromURL(url string) string {
    parts := strings.SplitN(url, ".net/", 2)
    if len(parts) < 2 { return url }
    p := strings.SplitN(parts[1], "?", 2)[0]
    return "/" + p
}

func classifyToWA(t string) whatsmeow.MediaType {
    switch t {
    case "image": return whatsmeow.MediaImage
    case "video": return whatsmeow.MediaVideo
    case "audio": return whatsmeow.MediaAudio
    case "document": return whatsmeow.MediaDocument
    default: return whatsmeow.MediaDocument
    }
}

