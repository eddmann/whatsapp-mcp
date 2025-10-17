package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/eddmann/whatsapp-mcp/internal/config"
	"github.com/eddmann/whatsapp-mcp/internal/domain"
	"github.com/eddmann/whatsapp-mcp/internal/media"
	"github.com/eddmann/whatsapp-mcp/internal/service"
	"github.com/eddmann/whatsapp-mcp/internal/store"
	"github.com/eddmann/whatsapp-mcp/internal/wa"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	// Initialize logger
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))

	// Configure ffmpeg if specified
	if cfg.FFmpegPath != "" {
		media.SetFFmpegPath(cfg.FFmpegPath)
	}

	// Startup log
	logger.Info("startup",
		"db_dir", cfg.DBDir,
		"log_level", cfg.LogLevelString(),
		"ffmpeg", cfg.FFmpegPath,
	)

	// Open SQLite store (messages.db)
	db, err := store.Open(cfg.DBDir)
	if err != nil {
		logger.Error("failed to open store", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	// Initialize WhatsApp client (connect deferred until after MCP is ready)
	waclient, err := wa.New(db, cfg.DBDir, cfg.LogLevelString(), logger)
	if err != nil {
		logger.Error("failed to init wa client", "err", err)
		os.Exit(1)
	}

	// Initialize services
	chatService := service.NewChatService(db)
	messageService := service.NewMessageService(db, waclient)

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
        opts := domain.ListChatsOptions{
            Query:              mcp.ParseString(req, "query", ""),
            Limit:              mcp.ParseInt(req, "limit", 20),
            Page:               mcp.ParseInt(req, "page", 0),
            SortBy:             mcp.ParseString(req, "sort_by", "last_active"),
            IncludeLastMessage: mcp.ParseBoolean(req, "include_last_message", true),
        }
        chats, err := chatService.ListChats(opts)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"ok": false, "error": "list_chats failed", "details": err.Error()}), nil }
        return mcp.NewToolResultJSON(map[string]any{"chats": chats})
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
        query := mcp.ParseString(req, "query", "")
        contacts, err := chatService.SearchContacts(query)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"ok": false, "error": "search_contacts failed", "details": err.Error()}), nil }
        return mcp.NewToolResultJSON(map[string]any{"contacts": contacts})
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
        includeLast := mcp.ParseBoolean(req, "include_last_message", true)
        chat, err := chatService.GetChat(jid, includeLast)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"ok": false, "error": "get_chat failed", "details": err.Error()}), nil }
        return mcp.NewToolResultJSON(chat)
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
        opts := domain.ListMessagesOptions{
            After:          mcp.ParseString(req, "after", ""),
            Before:         mcp.ParseString(req, "before", ""),
            Sender:         mcp.ParseString(req, "sender_phone_number", ""),
            ChatJID:        mcp.ParseString(req, "chat_jid", ""),
            Query:          mcp.ParseString(req, "query", ""),
            Limit:          mcp.ParseInt(req, "limit", 20),
            Page:           mcp.ParseInt(req, "page", 0),
            IncludeContext: mcp.ParseBoolean(req, "include_context", true),
            ContextBefore:  mcp.ParseInt(req, "context_before", 1),
            ContextAfter:   mcp.ParseInt(req, "context_after", 1),
        }
        messages, err := messageService.ListMessages(opts)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"ok": false, "error": "list_messages failed", "details": err.Error()}), nil }
        return mcp.NewToolResultJSON(map[string]any{"messages": messages})
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
        context, err := messageService.GetMessageContext(id, before, after)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"ok": false, "error": "get_message_context failed", "details": err.Error()}), nil }
        return mcp.NewToolResultJSON(context)
    })

    // get_direct_chat_by_contact
    srv.AddTool(mcp.NewTool(
        "get_direct_chat_by_contact",
        mcp.WithDescription("Get a direct chat by sender phone number (excludes groups)."),
        mcp.WithString("sender_phone_number", mcp.Required(), mcp.Description("Phone number with country code (no +)")),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        phone := mcp.ParseString(req, "sender_phone_number", "")
        chat, err := chatService.GetDirectChatByContact(phone)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"ok": false, "error": "get_direct_chat_by_contact failed", "details": err.Error()}), nil }
        return mcp.NewToolResultJSON(chat)
    })

    // get_contact_chats
    srv.AddTool(mcp.NewTool(
        "get_contact_chats",
        mcp.WithDescription("List chats involving the given contact JID."),
        mcp.WithString("jid", mcp.Required(), mcp.Description("Contact JID")),
        mcp.WithNumber("limit", mcp.Description("Max results"), mcp.DefaultNumber(20), mcp.Min(1), mcp.Max(200)),
        mcp.WithNumber("page", mcp.Description("Page number"), mcp.DefaultNumber(0), mcp.Min(0)),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        jid := mcp.ParseString(req, "jid", "")
        limit := mcp.ParseInt(req, "limit", 20)
        page := mcp.ParseInt(req, "page", 0)
        chats, err := chatService.GetContactChats(jid, limit, page)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"ok": false, "error": "get_contact_chats failed", "details": err.Error()}), nil }
        return mcp.NewToolResultJSON(map[string]any{"chats": chats})
    })

    // get_last_interaction
    srv.AddTool(mcp.NewTool(
        "get_last_interaction",
        mcp.WithDescription("Get the most recent message involving the contact"),
        mcp.WithString("jid", mcp.Required(), mcp.Description("Contact JID")),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        jid := mcp.ParseString(req, "jid", "")
        message, err := messageService.GetLastInteraction(jid)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"ok": false, "error": "get_last_interaction failed", "details": err.Error()}), nil }
        return mcp.NewToolResultJSON(map[string]any{"message": message})
    })

    // search_messages
    srv.AddTool(mcp.NewTool(
        "search_messages",
        mcp.WithDescription("Full-text search across message content (uses FTS5 if available)."),
        mcp.WithString("query", mcp.Required(), mcp.Description("Search query string")),
        mcp.WithNumber("limit", mcp.Description("Max results"), mcp.DefaultNumber(20), mcp.Min(1), mcp.Max(200)),
        mcp.WithNumber("page", mcp.Description("Page number"), mcp.DefaultNumber(0), mcp.Min(0)),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        opts := domain.SearchMessagesOptions{
            Query: mcp.ParseString(req, "query", ""),
            Limit: mcp.ParseInt(req, "limit", 20),
            Page:  mcp.ParseInt(req, "page", 0),
        }
        messages, err := messageService.SearchMessages(opts)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"ok": false, "error": "search_messages failed", "details": err.Error()}), nil }
        return mcp.NewToolResultJSON(map[string]any{"messages": messages})
    })

    // send_message
    srv.AddTool(mcp.NewTool(
        "send_message",
        mcp.WithDescription("Send a text message to a phone number or chat JID."),
        mcp.WithString("recipient", mcp.Required(), mcp.Description("Phone number (no +) or chat JID")),
        mcp.WithString("message", mcp.Required(), mcp.Description("Text message body")),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        recipient := mcp.ParseString(req, "recipient", "")
        message := mcp.ParseString(req, "message", "")
        result, err := messageService.SendText(recipient, message)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"success": false, "message": err.Error()}), nil }
        return mcp.NewToolResultJSON(result)
    })

    // send_file
    srv.AddTool(mcp.NewTool(
        "send_file",
        mcp.WithDescription("Send a file (image, video, document) to a recipient."),
        mcp.WithString("recipient", mcp.Required(), mcp.Description("Phone number (no +) or chat JID")),
        mcp.WithString("media_path", mcp.Required(), mcp.Description("Absolute path to local file")),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        recipient := mcp.ParseString(req, "recipient", "")
        mediaPath := mcp.ParseString(req, "media_path", "")
        result, err := messageService.SendMedia(recipient, mediaPath)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"success": false, "message": err.Error()}), nil }
        return mcp.NewToolResultJSON(result)
    })

    // send_audio_message
    srv.AddTool(mcp.NewTool(
        "send_audio_message",
        mcp.WithDescription("Send an audio file as WhatsApp voice message (Opus .ogg preferred)."),
        mcp.WithString("recipient", mcp.Required(), mcp.Description("Phone number (no +) or chat JID")),
        mcp.WithString("media_path", mcp.Required(), mcp.Description("Absolute path to audio file (converted if needed)")),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        recipient := mcp.ParseString(req, "recipient", "")
        mediaPath := mcp.ParseString(req, "media_path", "")
        result, err := messageService.SendMedia(recipient, mediaPath)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"success": false, "message": err.Error()}), nil }
        return mcp.NewToolResultJSON(result)
    })

    // download_media
    srv.AddTool(mcp.NewTool(
        "download_media",
        mcp.WithDescription("Download media for a message and return the local file path."),
        mcp.WithString("message_id", mcp.Required(), mcp.Description("Message ID that contains media")),
        mcp.WithString("chat_jid", mcp.Required(), mcp.Description("Chat JID containing the message")),
    ), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        messageID := mcp.ParseString(req, "message_id", "")
        chatJID := mcp.ParseString(req, "chat_jid", "")
        result, err := messageService.DownloadMedia(messageID, chatJID)
        if err != nil { return mcp.NewToolResultStructuredOnly(map[string]any{"success": false, "message": err.Error()}), nil }
        return mcp.NewToolResultJSON(result)
    })

    // Connect to WhatsApp (prints QR on first run)
    go func() {
        ctx, cancel := context.WithTimeout(context.Background(), cfg.WhatsApp.QRTimeout)
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

