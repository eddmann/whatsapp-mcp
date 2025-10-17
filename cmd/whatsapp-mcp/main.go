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
		mcp.WithDescription("List recent WhatsApp conversations with message previews. Use this to: 1) Show user their recent chats, 2) Find a specific conversation by name or phone, 3) Get chat JIDs for other operations. Supports search by name/JID, sorting by activity or alphabetically, and pagination."),
		mcp.WithString("query",
			mcp.Description("Search term to filter chats by name or JID. Examples: 'mom', '44123', 'work group'. Case-insensitive partial match."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of chats to return (1-200)"),
			mcp.DefaultNumber(20),
			mcp.Min(1),
			mcp.Max(200),
		),
		mcp.WithNumber("page",
			mcp.Description("Page number for pagination, 0-based. Use with limit to browse through large chat lists."),
			mcp.DefaultNumber(0),
			mcp.Min(0),
		),
		mcp.WithBoolean("include_last_message",
			mcp.Description("Include the last message content, sender, and direction (from me or to me) for each chat"),
			mcp.DefaultBool(true),
		),
		mcp.WithString("sort_by",
			mcp.Description("Sort order: 'last_active' (most recent messages first) or 'name' (alphabetical)"),
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
		if err != nil {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "failed to list chats",
				"details": err.Error(),
				"hint":    "This may be a database error. Try again or check if the database is accessible.",
			}), nil
		}

		// Get total count for pagination metadata
		totalCount, _ := db.CountChats(opts.Query)

		return mcp.NewToolResultJSON(map[string]any{
			"success":  true,
			"chats":    chats,
			"total":    totalCount,
			"page":     opts.Page,
			"limit":    opts.Limit,
			"has_more": (opts.Page+1)*opts.Limit < totalCount,
		})
	})

	// search_contacts
	srv.AddTool(mcp.NewTool(
		"search_contacts",
		mcp.WithDescription("Search for contacts (individuals, not groups) by name or phone number. Use this to find a specific person's contact information or JID before sending a message. Returns matching contacts with their JID, phone number, and name."),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Search term to match against contact name or phone number. Examples: 'john', '44123', 'alice'. Case-insensitive partial match."),
		),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query := mcp.ParseString(req, "query", "")
		contacts, err := chatService.SearchContacts(query)
		if err != nil {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "failed to search contacts",
				"details": err.Error(),
				"hint":    "Try a different search term or check if contacts are synced.",
			}), nil
		}
		return mcp.NewToolResultJSON(map[string]any{"success": true, "contacts": contacts})
	})

	// get_chat
	srv.AddTool(mcp.NewTool(
		"get_chat",
		mcp.WithDescription("Get detailed information about a specific chat by its JID. Use this when you have a chat JID and need to retrieve its metadata, name, and last message information."),
		mcp.WithString("chat_jid",
			mcp.Required(),
			mcp.Description("Chat JID to fetch. Format: '1234567890@s.whatsapp.net' for contacts or '123456@g.us' for groups."),
		),
		mcp.WithBoolean("include_last_message",
			mcp.Description("Include the last message content, sender, and timestamp in the response"),
			mcp.DefaultBool(true),
		),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		jid := mcp.ParseString(req, "chat_jid", "")
		includeLast := mcp.ParseBoolean(req, "include_last_message", true)
		chat, err := chatService.GetChat(jid, includeLast)
		if err != nil {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "chat not found",
				"details": err.Error(),
				"hint":    "Use list_chats or search_contacts to find available chat JIDs. The chat may not exist or have no message history.",
			}), nil
		}
		return mcp.NewToolResultJSON(map[string]any{"success": true, "chat": chat})
	})

	// list_messages
	srv.AddTool(mcp.NewTool(
		"list_messages",
		mcp.WithDescription("List messages from conversations with powerful filtering options. Use this to: 1) View conversation history with a person/group, 2) Get messages within a date range, 3) Find messages from a specific sender. Filters can be combined. Returns messages with content, sender, timestamp, chat name, and media type."),
		mcp.WithString("after", mcp.Description("ISO-8601 timestamp (e.g., '2025-01-15T00:00:00Z') - only messages after this time")),
		mcp.WithString("before", mcp.Description("ISO-8601 timestamp (e.g., '2025-01-20T23:59:59Z') - only messages before this time")),
		mcp.WithString("sender_phone_number", mcp.Description("Filter by sender phone number without '+' (e.g., '441234567890')")),
		mcp.WithString("chat_jid", mcp.Description("Filter to messages in a specific chat. Get JID from list_chats.")),
		mcp.WithString("query", mcp.Description("Substring search within message content (case-insensitive). For powerful search, use search_messages instead.")),
		mcp.WithBoolean("include_context", mcp.Description("Include surrounding messages for conversation flow. WARNING: Increases response size 3x. Best for <10 results."), mcp.DefaultBool(false)),
		mcp.WithNumber("context_before", mcp.Description("Number of messages before each result (when include_context=true)"), mcp.DefaultNumber(1), mcp.Min(0), mcp.Max(10)),
		mcp.WithNumber("context_after", mcp.Description("Number of messages after each result (when include_context=true)"), mcp.DefaultNumber(1), mcp.Min(0), mcp.Max(10)),
		mcp.WithNumber("limit", mcp.Description("Maximum messages to return (1-200)"), mcp.DefaultNumber(20), mcp.Min(1), mcp.Max(200)),
		mcp.WithNumber("page", mcp.Description("Page number for pagination, 0-based"), mcp.DefaultNumber(0), mcp.Min(0)),
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
		if err != nil {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "failed to list messages",
				"details": err.Error(),
				"hint":    "Check your filter parameters. Ensure chat_jid is valid and timestamps are in ISO-8601 format.",
			}), nil
		}
		return mcp.NewToolResultJSON(map[string]any{"success": true, "messages": messages})
	})

	// get_message_context
	srv.AddTool(mcp.NewTool(
		"get_message_context",
		mcp.WithDescription("Get surrounding messages around a specific message to understand conversation context. Use this when you have a message ID and need to see what was said before and after it."),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("Target message ID to get context around")),
		mcp.WithNumber("before", mcp.Description("Number of messages to include before the target message"), mcp.DefaultNumber(5), mcp.Min(0), mcp.Max(10)),
		mcp.WithNumber("after", mcp.Description("Number of messages to include after the target message"), mcp.DefaultNumber(5), mcp.Min(0), mcp.Max(10)),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "message_id", "")
		before := mcp.ParseInt(req, "before", 5)
		after := mcp.ParseInt(req, "after", 5)
		context, err := messageService.GetMessageContext(id, before, after)
		if err != nil {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "message not found",
				"details": err.Error(),
				"hint":    "The message ID may be invalid or the message may have been deleted. Get message IDs from list_messages or search_messages.",
			}), nil
		}
		return mcp.NewToolResultJSON(map[string]any{"success": true, "context": context})
	})

	// get_direct_chat_by_contact
	srv.AddTool(mcp.NewTool(
		"get_direct_chat_by_contact",
		mcp.WithDescription("Get a direct message chat (not a group) by phone number. Use this to find the chat JID for a contact when you only have their phone number."),
		mcp.WithString("sender_phone_number", mcp.Required(), mcp.Description("Phone number with country code, no '+' sign (e.g., '441234567890')")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		phone := mcp.ParseString(req, "sender_phone_number", "")
		chat, err := chatService.GetDirectChatByContact(phone)
		if err != nil {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "direct chat not found for this phone number",
				"details": err.Error(),
				"hint":    "No direct message chat exists with this phone number. Ensure the phone number format is correct (country code without '+', e.g., '441234567890').",
			}), nil
		}
		return mcp.NewToolResultJSON(map[string]any{"success": true, "chat": chat})
	})

	// get_contact_chats
	srv.AddTool(mcp.NewTool(
		"get_contact_chats",
		mcp.WithDescription("List all chats (both direct messages and groups) involving a specific contact. Use this to see which group chats a person participates in, or to find all conversations with someone."),
		mcp.WithString("jid", mcp.Required(), mcp.Description("Contact JID (e.g., '441234567890@s.whatsapp.net')")),
		mcp.WithNumber("limit", mcp.Description("Maximum chats to return (1-200)"), mcp.DefaultNumber(20), mcp.Min(1), mcp.Max(200)),
		mcp.WithNumber("page", mcp.Description("Page number for pagination, 0-based"), mcp.DefaultNumber(0), mcp.Min(0)),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		jid := mcp.ParseString(req, "jid", "")
		limit := mcp.ParseInt(req, "limit", 20)
		page := mcp.ParseInt(req, "page", 0)
		chats, err := chatService.GetContactChats(jid, limit, page)
		if err != nil {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "failed to get chats for contact",
				"details": err.Error(),
				"hint":    "Ensure the JID format is correct (e.g., '441234567890@s.whatsapp.net'). Use search_contacts to find valid JIDs.",
			}), nil
		}
		return mcp.NewToolResultJSON(map[string]any{"success": true, "chats": chats})
	})

	// get_last_interaction
	srv.AddTool(mcp.NewTool(
		"get_last_interaction",
		mcp.WithDescription("Get the most recent message sent to or from a specific contact across all chats. Use this to quickly check the last interaction with someone."),
		mcp.WithString("jid", mcp.Required(), mcp.Description("Contact JID (e.g., '441234567890@s.whatsapp.net')")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		jid := mcp.ParseString(req, "jid", "")
		message, err := messageService.GetLastInteraction(jid)
		if err != nil {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "no interactions found with this contact",
				"details": err.Error(),
				"hint":    "No message history exists with this contact. Verify the JID is correct.",
			}), nil
		}
		return mcp.NewToolResultJSON(map[string]any{"success": true, "message": message})
	})

	// search_messages
	srv.AddTool(mcp.NewTool(
		"search_messages",
		mcp.WithDescription("Search message content across all conversations using full-text search (FTS5). Use this to find specific information in message history. Search syntax: simple keywords ('vacation'), exact phrases ('\"project meeting\"'), boolean operators ('vacation OR holiday', 'vacation AND photos'), exclusion ('vacation -work'), prefix wildcard ('vacat*'). Returns matching messages with chat name, sender, timestamp, and content."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search query string. Use simple keywords for best results. Examples: 'vacation', '\"project meeting\"', 'vacation OR holiday'. Supports FTS5 operators for advanced queries.")),
		mcp.WithNumber("limit", mcp.Description("Maximum results to return (1-200)"), mcp.DefaultNumber(20), mcp.Min(1), mcp.Max(200)),
		mcp.WithNumber("page", mcp.Description("Page number for pagination, 0-based"), mcp.DefaultNumber(0), mcp.Min(0)),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		opts := domain.SearchMessagesOptions{
			Query: mcp.ParseString(req, "query", ""),
			Limit: mcp.ParseInt(req, "limit", 20),
			Page:  mcp.ParseInt(req, "page", 0),
		}
		messages, err := messageService.SearchMessages(opts)
		if err != nil {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "search failed",
				"details": err.Error(),
				"hint":    "Try simplifying your search query. Use simple keywords first, then try advanced FTS5 operators if needed.",
			}), nil
		}
		return mcp.NewToolResultJSON(map[string]any{"success": true, "messages": messages})
	})

	// send_message - Unified tool for sending text, media, or both
	srv.AddTool(mcp.NewTool(
		"send_message",
		mcp.WithDescription("Send a message to a WhatsApp contact or group. Can send text only, media only (image/video/audio/document), or media with caption. For contacts, use phone number without '+'. For groups, use chat JID from list_chats. Audio files are sent as voice messages (PTT) and automatically converted to Opus if needed."),
		mcp.WithString("recipient", mcp.Required(), mcp.Description("Phone number without '+' (e.g., '441234567890') or group JID (e.g., '123456@g.us')")),
		mcp.WithString("text", mcp.Description("Message text. If media_path provided, becomes caption for the media. If no media_path, sent as text message. Optional for media-only messages.")),
		mcp.WithString("media_path", mcp.Description("Absolute path to media file. Supports images (jpg/png), videos (mp4), audio (ogg/mp3/wav/m4a), documents (pdf/docx). Audio sent as voice messages (PTT).")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		recipient := mcp.ParseString(req, "recipient", "")
		text := mcp.ParseString(req, "text", "")
		mediaPath := mcp.ParseString(req, "media_path", "")

		// Validation
		if recipient == "" {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "recipient parameter is required",
				"hint":    "Provide a phone number (e.g., '441234567890') or group JID (e.g., '123456@g.us'). Use list_chats to find available recipients.",
			}), nil
		}

		if text == "" && mediaPath == "" {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "either 'text' or 'media_path' must be provided",
				"hint":    "Provide message text, a media file path, or both (media with caption).",
			}), nil
		}

		// Determine what to send
		var result *domain.SendResult
		var err error

		if mediaPath != "" {
			// Sending media (text becomes caption if provided)
			result, err = messageService.SendMedia(recipient, mediaPath, text)
			if err != nil {
				return mcp.NewToolResultStructuredOnly(map[string]any{
					"success": false,
					"error":   "failed to send media",
					"details": err.Error(),
					"hint":    "Check that the file exists and is readable. For audio files, ensure ffmpeg is installed. Verify WhatsApp connection with get_connection_status.",
				}), nil
			}
		} else {
			// Sending text only
			result, err = messageService.SendText(recipient, text)
			if err != nil {
				return mcp.NewToolResultStructuredOnly(map[string]any{
					"success": false,
					"error":   "failed to send message",
					"details": err.Error(),
					"hint":    "Check WhatsApp connection with get_connection_status. Ensure recipient format is correct and WhatsApp is connected.",
				}), nil
			}
		}

		return mcp.NewToolResultJSON(result)
	})

	// download_media
	srv.AddTool(mcp.NewTool(
		"download_media",
		mcp.WithDescription("Download media (image, video, audio, document) from a message to local storage. Returns the file path where the media was saved. Use this to access media content from messages."),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("Message ID that contains the media to download")),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description("Chat JID containing the message (e.g., '441234567890@s.whatsapp.net')")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		messageID := mcp.ParseString(req, "message_id", "")
		chatJID := mcp.ParseString(req, "chat_jid", "")

		if messageID == "" {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "message_id parameter is required",
				"hint":    "Provide the message ID from list_messages or search_messages that contains media.",
			}), nil
		}
		if chatJID == "" {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "chat_jid parameter is required",
				"hint":    "Provide the chat JID where the message is located. Get this from the message or list_chats.",
			}), nil
		}

		result, err := messageService.DownloadMedia(messageID, chatJID)
		if err != nil {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "failed to download media",
				"details": err.Error(),
				"hint":    "Ensure the message contains media (check media_type field). The media may have expired or been deleted from WhatsApp servers.",
			}), nil
		}
		return mcp.NewToolResultJSON(result)
	})

	// get_connection_status
	srv.AddTool(mcp.NewTool(
		"get_connection_status",
		mcp.WithDescription("Check WhatsApp connection status and server health. Returns connection state, login status, and database statistics. Use this to verify the server is ready before sending messages or to debug connection issues."),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		status := map[string]any{
			"connected":      false,
			"logged_in":      false,
			"server_running": true,
		}

		if waclient.WA != nil {
			status["connected"] = waclient.WA.IsConnected()
			status["logged_in"] = waclient.WA.IsLoggedIn()

			if waclient.WA.Store != nil && waclient.WA.Store.ID != nil {
				status["device"] = map[string]any{
					"user":   waclient.WA.Store.ID.User,
					"device": waclient.WA.Store.ID.Device,
				}
			}
		}

		// Get database stats
		var chatCount, messageCount int
		_ = db.Messages.QueryRow("SELECT COUNT(*) FROM chats").Scan(&chatCount)
		_ = db.Messages.QueryRow("SELECT COUNT(*) FROM messages").Scan(&messageCount)

		status["database"] = map[string]any{
			"chats":    chatCount,
			"messages": messageCount,
		}

		return mcp.NewToolResultJSON(map[string]any{"status": status})
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
