package main

import (
	"fmt"
	"os"

	"context"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	// mcphelpers moved into store package
	"github.com/eddmann/whatsapp-mcp/internal/media"
	"github.com/eddmann/whatsapp-mcp/internal/store"
	"github.com/eddmann/whatsapp-mcp/internal/wa"
)

// Thin adapters will be added as we migrate each tool.

func main() {
	dbDir := os.Getenv("DB_DIR")
	if dbDir == "" {
		dbDir = "store"
	}
    logLevel := os.Getenv("LOG_LEVEL")
    ffmpegPath := os.Getenv("FFMPEG_PATH")
    if ffmpegPath != "" {
        media.SetFFmpegPath(ffmpegPath)
    }

    // Normalize log level and create app logger (slog)
    lvl := logLevel
    if lvl == "" { lvl = "INFO" }
    switch lvl {
    case "DEBUG", "INFO", "WARN", "ERROR", "debug", "info", "warn", "error":
        // ok
    default:
        lvl = "INFO"
    }
    if lvl == "debug" { lvl = "DEBUG" } else if lvl == "info" { lvl = "INFO" } else if lvl == "warn" { lvl = "WARN" } else if lvl == "error" { lvl = "ERROR" }
    var slogLevel slog.Level
    switch lvl {
    case "DEBUG": slogLevel = slog.LevelDebug
    case "WARN": slogLevel = slog.LevelWarn
    case "ERROR": slogLevel = slog.LevelError
    default: slogLevel = slog.LevelInfo
    }
    logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slogLevel}))

	// Open SQLite store (messages.db)
    db, err := store.Open(dbDir)
	if err != nil {
        logger.Error("failed to open store", "err", err)
        os.Exit(1)
	}
	defer db.Close()

    // Initialize WhatsApp client (connect deferred until after MCP is ready)
    waclient, err := wa.New(db, dbDir, lvl, logger)
	if err != nil {
        logger.Error("failed to init wa client", "err", err)
        os.Exit(1)
	}

    // Startup configuration log
    fp := ffmpegPath
    if fp == "" { fp = "ffmpeg" }
    logger.Info("startup", "db_dir", dbDir, "log_level", lvl, "ffmpeg", fp)

    // Initialize mcp-go stdio server
    srv := server.NewMCPServer(
        "whatsapp",
        "1.0.0",
        server.WithToolCapabilities(true),
    )

    // list_chats
    srv.AddTool(mcp.NewTool(
        "list_chats",
        mcp.WithDescription("List chats with optional query, pagination and sorting."),
        mcp.WithString("query",
            mcp.Description("Optional search across chat name or JID"),
        ),
        mcp.WithNumber("limit",
            mcp.Description("Max results to return"),
            mcp.DefaultNumber(20),
            mcp.Min(1),
            mcp.Max(200),
        ),
        mcp.WithNumber("page",
            mcp.Description("Page number (0-based)"),
            mcp.DefaultNumber(0),
            mcp.Min(0),
        ),
        mcp.WithBoolean("include_last_message",
            mcp.Description("Include last message metadata for each chat"),
            mcp.DefaultBool(true),
        ),
        mcp.WithString("sort_by",
            mcp.Description("Sort order: last_active or name"),
            mcp.Enum("last_active", "name"),
            mcp.DefaultString("last_active"),
        ),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        q := mcp.ParseString(req, "query", "")
        limit := mcp.ParseInt(req, "limit", 20)
        page := mcp.ParseInt(req, "page", 0)
        sortBy := mcp.ParseString(req, "sort_by", "last_active")
        includeLast := mcp.ParseBoolean(req, "include_last_message", true)
        res, err := db.MListChats(q, limit, page, sortBy, includeLast)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"ok": false, "error": "list_chats failed", "details": err.Error()}), nil }
        return mcp.NewToolResultJSON(res)
    })

    // search_contacts
    srv.AddTool(mcp.NewTool(
        "search_contacts",
        mcp.WithDescription("Search contacts by name or JID (excludes groups)."),
        mcp.WithString("query",
            mcp.Required(),
            mcp.Description("Search text to match against name/JID"),
        ),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        q := mcp.ParseString(req, "query", "")
        res, err := db.MSearchContacts(q)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"ok": false, "error": "search_contacts failed", "details": err.Error()}), nil }
        return mcp.NewToolResultJSON(res)
    })

    // get_chat
    srv.AddTool(mcp.NewTool(
        "get_chat",
        mcp.WithDescription("Get chat metadata by JID."),
        mcp.WithString("chat_jid",
            mcp.Required(),
            mcp.Description("Chat JID to fetch"),
        ),
        mcp.WithBoolean("include_last_message",
            mcp.Description("Include last message metadata in the result"),
            mcp.DefaultBool(true),
        ),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        jid := mcp.ParseString(req, "chat_jid", "")
        inc := mcp.ParseBoolean(req, "include_last_message", true)
        res, err := db.MGetChat(jid, inc)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"ok": false, "error": "get_chat failed", "details": err.Error()}), nil }
        return mcp.NewToolResultJSON(res)
    })

    // list_messages
    srv.AddTool(mcp.NewTool(
        "list_messages",
        mcp.WithDescription("List messages with filters and pagination."),
        mcp.WithString("after", mcp.Description("ISO-8601 timestamp to include messages after")),
        mcp.WithString("before", mcp.Description("ISO-8601 timestamp to include messages before")),
        mcp.WithString("sender_phone_number", mcp.Description("Filter by sender phone number (no +)")),
        mcp.WithString("chat_jid", mcp.Description("Filter by chat JID")),
        mcp.WithString("query", mcp.Description("Substring search within message content")),
        mcp.WithBoolean("include_context", mcp.Description("Include context around results"), mcp.DefaultBool(true)),
        mcp.WithNumber("context_before", mcp.Description("Messages before"), mcp.DefaultNumber(1), mcp.Min(0), mcp.Max(100)),
        mcp.WithNumber("context_after", mcp.Description("Messages after"), mcp.DefaultNumber(1), mcp.Min(0), mcp.Max(100)),
        mcp.WithNumber("limit", mcp.Description("Max results"), mcp.DefaultNumber(20), mcp.Min(1), mcp.Max(200)),
        mcp.WithNumber("page", mcp.Description("Page (0-based)"), mcp.DefaultNumber(0), mcp.Min(0)),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        after := mcp.ParseString(req, "after", "")
        before := mcp.ParseString(req, "before", "")
        sender := mcp.ParseString(req, "sender_phone_number", "")
        chatJID := mcp.ParseString(req, "chat_jid", "")
        query := mcp.ParseString(req, "query", "")
        includeCtx := mcp.ParseBoolean(req, "include_context", true)
        ctxBefore := mcp.ParseInt(req, "context_before", 1)
        ctxAfter := mcp.ParseInt(req, "context_after", 1)
        limit := mcp.ParseInt(req, "limit", 20)
        page := mcp.ParseInt(req, "page", 0)
        res, err := db.MListMessages(after, before, sender, chatJID, query, limit, page, includeCtx, ctxBefore, ctxAfter)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"ok": false, "error": "list_messages failed", "details": err.Error()}), nil }
        return mcp.NewToolResultJSON(res)
    })

    // get_message_context
    srv.AddTool(mcp.NewTool(
        "get_message_context",
        mcp.WithDescription("Get messages around a specific message ID."),
        mcp.WithString("message_id", mcp.Required(), mcp.Description("Target message ID")),
        mcp.WithNumber("before", mcp.Description("Messages before"), mcp.DefaultNumber(5), mcp.Min(0), mcp.Max(100)),
        mcp.WithNumber("after", mcp.Description("Messages after"), mcp.DefaultNumber(5), mcp.Min(0), mcp.Max(100)),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        id := mcp.ParseString(req, "message_id", "")
        before := mcp.ParseInt(req, "before", 5)
        after := mcp.ParseInt(req, "after", 5)
        res, err := db.MGetMessageContext(id, before, after)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"ok": false, "error": "get_message_context failed", "details": err.Error()}), nil }
        return mcp.NewToolResultJSON(res)
    })

    // get_direct_chat_by_contact
    srv.AddTool(mcp.NewTool(
        "get_direct_chat_by_contact",
        mcp.WithDescription("Get a direct chat by sender phone number (excludes groups)."),
        mcp.WithString("sender_phone_number", mcp.Required(), mcp.Description("Phone number with country code (no +)")),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        ph := mcp.ParseString(req, "sender_phone_number", "")
        res, err := db.MGetDirectChatByContact(ph)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"ok": false, "error": "get_direct_chat_by_contact failed", "details": err.Error()}), nil }
        return mcp.NewToolResultJSON(res)
    })

    // get_contact_chats
    srv.AddTool(mcp.NewTool(
        "get_contact_chats",
        mcp.WithDescription("List chats involving the given contact JID."),
        mcp.WithString("jid", mcp.Required(), mcp.Description("Contact JID")),
        mcp.WithNumber("limit", mcp.Description("Max results"), mcp.DefaultNumber(20), mcp.Min(1), mcp.Max(200)),
        mcp.WithNumber("page", mcp.Description("Page number"), mcp.DefaultNumber(0), mcp.Min(0)),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        j := mcp.ParseString(req, "jid", "")
        l := mcp.ParseInt(req, "limit", 20)
        pg := mcp.ParseInt(req, "page", 0)
        res, err := db.MGetContactChats(j, l, pg)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"ok": false, "error": "get_contact_chats failed", "details": err.Error()}), nil }
        return mcp.NewToolResultJSON(res)
    })

    // get_last_interaction
    srv.AddTool(mcp.NewTool(
        "get_last_interaction",
        mcp.WithDescription("Get the most recent message involving the contact"),
        mcp.WithString("jid", mcp.Required(), mcp.Description("Contact JID")),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        j := mcp.ParseString(req, "jid", "")
        res, err := db.MGetLastInteraction(j)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"ok": false, "error": "get_last_interaction failed", "details": err.Error()}), nil }
        return mcp.NewToolResultJSON(res)
    })

    // search_messages
    srv.AddTool(mcp.NewTool(
        "search_messages",
        mcp.WithDescription("Full-text search across message content (uses FTS5 if available)."),
        mcp.WithString("query", mcp.Required(), mcp.Description("Search query string")),
        mcp.WithNumber("limit", mcp.Description("Max results"), mcp.DefaultNumber(20), mcp.Min(1), mcp.Max(200)),
        mcp.WithNumber("page", mcp.Description("Page number"), mcp.DefaultNumber(0), mcp.Min(0)),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        q := mcp.ParseString(req, "query", "")
        l := mcp.ParseInt(req, "limit", 20)
        pg := mcp.ParseInt(req, "page", 0)
        res, err := db.MSearchMessages(q, l, pg)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"ok": false, "error": "search_messages failed", "details": err.Error()}), nil }
        return mcp.NewToolResultJSON(res)
    })

    // send_message
    srv.AddTool(mcp.NewTool(
        "send_message",
        mcp.WithDescription("Send a text message to a phone number or chat JID."),
        mcp.WithString("recipient", mcp.Required(), mcp.Description("Phone number (no +) or chat JID")),
        mcp.WithString("message", mcp.Required(), mcp.Description("Text message body")),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        r := mcp.ParseString(req, "recipient", "")
        m := mcp.ParseString(req, "message", "")
        ok, msg, err := waclient.SendText(r, m)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"success": false, "message": err.Error()}), nil }
        return mcp.NewToolResultJSON(map[string]any{"success": ok, "message": msg})
    })

    // send_file
    srv.AddTool(mcp.NewTool(
        "send_file",
        mcp.WithDescription("Send a file (image, video, document) to a recipient."),
        mcp.WithString("recipient", mcp.Required(), mcp.Description("Phone number (no +) or chat JID")),
        mcp.WithString("media_path", mcp.Required(), mcp.Description("Absolute path to local file")),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        r := mcp.ParseString(req, "recipient", "")
        pth := mcp.ParseString(req, "media_path", "")
        ok, msg, err := waclient.SendMedia(r, pth)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"success": false, "message": err.Error()}), nil }
        return mcp.NewToolResultJSON(map[string]any{"success": ok, "message": msg})
    })

    // send_audio_message
    srv.AddTool(mcp.NewTool(
        "send_audio_message",
        mcp.WithDescription("Send an audio file as WhatsApp voice message (Opus .ogg preferred)."),
        mcp.WithString("recipient", mcp.Required(), mcp.Description("Phone number (no +) or chat JID")),
        mcp.WithString("media_path", mcp.Required(), mcp.Description("Absolute path to audio file (converted if needed)")),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        r := mcp.ParseString(req, "recipient", "")
        pth := mcp.ParseString(req, "media_path", "")
        ok, msg, err := waclient.SendMedia(r, pth)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"success": false, "message": err.Error()}), nil }
        return mcp.NewToolResultJSON(map[string]any{"success": ok, "message": msg})
    })

    // download_media
    srv.AddTool(mcp.NewTool(
        "download_media",
        mcp.WithDescription("Download media for a message and return the local file path."),
        mcp.WithString("message_id", mcp.Required(), mcp.Description("Message ID that contains media")),
        mcp.WithString("chat_jid", mcp.Required(), mcp.Description("Chat JID containing the message")),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        id := mcp.ParseString(req, "message_id", "")
        j := mcp.ParseString(req, "chat_jid", "")
        ok, t, fn, path, err := waclient.DownloadMedia(id, j)
        if err != nil { return mcp.NewToolResultJSON(map[string]any{"success": ok, "message": err.Error()}) }
        return mcp.NewToolResultJSON(map[string]any{"success": ok, "message": fmt.Sprintf("downloaded %s", t), "filename": fn, "path": path})
    })

    // Connect to WhatsApp (prints QR on first run)
    go func() {
        ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
        defer cancel()
        if err := waclient.ConnectWithQR(ctx); err != nil {
            logger.Error("WA connect error", "err", err)
        }
    }()

    // Graceful shutdown: capture SIGINT/SIGTERM and disconnect WA + close DBs
    stopped := make(chan struct{})
    sigc := make(chan os.Signal, 1)
    signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)

    go func() {
        <-sigc
        // Attempt clean disconnect
        if waclient != nil && waclient.WA != nil && waclient.WA.IsConnected() {
            waclient.WA.Disconnect()
        }
        _ = db.Close()
        close(stopped)
    }()

    // Serve stdio (blocks). Exit when stopped is closed
    go func() {
        if err := server.ServeStdio(srv); err != nil {
            logger.Error("MCP stdio error", "err", err)
        }
        // If stdio server exits, also trigger shutdown path
        sigc <- syscall.SIGINT
    }()

    <-stopped
    logger.Info("shutdown complete")
}

