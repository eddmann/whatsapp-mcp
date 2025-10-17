# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

WhatsApp MCP is a Go-based Model Context Protocol (MCP) server that connects to WhatsApp via whatsmeow, persists messages/chats to SQLite with FTS5, and exposes MCP tools over stdio for AI assistants like Claude/Cursor.

## Build Commands

### Standard Build

```bash
make build
```

Builds with CGO enabled and SQLite FTS5 support to `bin/whatsapp-mcp`. **CGO is required** for go-sqlite3.

### Build and Run

```bash
make run
```

Builds and executes the binary. On first run, displays a QR code for WhatsApp pairing.

### Clean

```bash
make clean
```

Removes the compiled binary.

### Format

```bash
make format
```

Formats Go source code using `go fmt`.

### Alternative Build (without Makefile)

```bash
CGO_ENABLED=1 go build -tags "sqlite_fts5" -o bin/whatsapp-mcp ./cmd/whatsapp-mcp
```

## Architecture

### Core Components

**cmd/whatsapp-mcp/main.go**

- Entry point: initializes store, WhatsApp client, services, and MCP server
- Registers 14 MCP tools covering chats, messages, search, messaging, media, and status
- Handles graceful shutdown (SIGINT/SIGTERM) to disconnect WhatsApp and close DBs
- Runs WhatsApp connection in background goroutine with QR authentication
- Serves MCP over stdio using mark3labs/mcp-go

**Tools registered:**
- Chat management: `list_chats`, `get_chat`, `search_contacts`, `get_direct_chat_by_contact`, `get_contact_chats`
- Message operations: `list_messages`, `get_message_context`, `get_last_interaction`, `search_messages`
- Messaging: `send_message` (unified tool for text, media, or both with fuzzy name matching)
- Media: `download_media`
- Status: `get_connection_status`

**internal/wa/client.go**

- Wraps whatsmeow.Client with store integration
- Provides core WhatsApp client initialization and handler registration

**internal/wa/sync.go**

- Event handlers: `handleMessage` persists incoming messages, `handleHistorySync` backfills history
- Processes WhatsApp events and syncs to local database

**internal/wa/resolver.go**

- Name resolution: `getChatName` and `resolvePreferredName` resolve JIDs to human-friendly names using contacts/groups
- Fuzzy recipient resolution: `ResolveRecipient` matches contact/group names to JIDs with disambiguation
- Backfill: `backfillChatNames` updates chats post-connect once contacts are available

**internal/wa/messaging.go**

- Message operations: `SendText`, `SendMedia` (with automatic ffmpeg conversion for non-.ogg audio), `DownloadMedia`
- Media classification by file extension (jpg → image, mp4 → video, ogg → audio PTT)

**internal/service/chat_service.go & message_service.go**

- Service layer providing business logic for chat and message operations
- Validates parameters and orchestrates store and client operations
- Methods correspond directly to MCP tool implementations

**internal/store/store.go**

- SQLite schema: `chats` table (jid, name, last_message_time) and `messages` table (id, chat_jid, sender, content, timestamp, media fields)
- FTS5 virtual table `messages_fts` for full-text search with triggers for auto-sync
- Migration enforces FTS5 availability and fails with clear error if not compiled in
- Database initialization and connection management

**internal/store/queries.go**

- All database query operations for chats and messages
- Methods: `ListChats`, `GetChat`, `SearchContacts`, `ListMessages`, `SearchMessages`, `GetMessageContext`, etc.
- Returns domain models (JSON-compatible structs) for MCP tool responses
- Context expansion: `ListMessages` can include before/after messages when `include_context=true`

**internal/config/config.go**

- Configuration management from environment variables
- Settings: `DB_DIR`, `LOG_LEVEL`, `FFMPEG_PATH`, WhatsApp QR timeout, MCP page size limits

**internal/domain/models.go**

- Domain models and option structs for all operations
- Types: `Chat`, `Message`, `Contact`, `SendResult`, `DownloadResult`, `MessageContext`
- Option types: `ListChatsOptions`, `ListMessagesOptions`, `SearchMessagesOptions`
- All structs are JSON-serializable for MCP responses

**internal/media/opus.go**

- `AnalyzeOggOpus`: parses Ogg Opus to extract duration and generate 64-byte waveform for WhatsApp PTT metadata
- Reads Ogg page headers and OpusHead to determine sample rate/preSkip

**internal/media/ffmpeg.go**

- `ConvertToOpusOgg`: converts any audio to Opus .ogg using ffmpeg (32kbps, 24kHz, VoIP mode)
- Uses configurable ffmpeg binary path via `SetFFmpegPath` (from FFMPEG_PATH env var)

### Data Flow

1. **Message Reception**: whatsmeow events → `handleMessage`/`handleHistorySync` (sync.go) → upsert `chats` and insert `messages` (queries.go) → FTS5 triggers update `messages_fts`
2. **Chat Name Resolution**: Check DB cache → extract from conversation metadata → query group info/contacts via whatsmeow → fallback to JID user part (resolver.go)
3. **Sending Messages**:
   - MCP tool call → service layer validation → fuzzy recipient resolution (resolver.go) → classify media type → upload via whatsmeow → construct proto message → send (messaging.go)
   - Fuzzy resolution: Check if phone/JID → search chat names in DB → return match or disambiguation prompt
4. **Audio Handling**: If not .ogg → ffmpeg convert (ffmpeg.go) → upload converted → analyze for duration/waveform (opus.go) → send as PTT
5. **Query Operations**: MCP tool → service layer → store queries (queries.go) → domain models → JSON response

### Database Schema

**chats**

- `jid` (PK): WhatsApp JID (e.g., `447123456789@s.whatsapp.net`, `abcdef@g.us`)
- `name`: Human-friendly name (resolved from contacts/groups)
- `last_message_time`: Timestamp of latest message

**messages**

- `(id, chat_jid)` (PK): Unique message ID per chat
- `sender`: Phone number or JID user part of sender
- `content`: Text content (or emoji summary for non-text types)
- `timestamp`: Message timestamp
- `is_from_me`: Boolean indicating if sent by authenticated user
- Media fields: `media_type`, `filename`, `url`, `media_key`, `file_sha256`, `file_enc_sha256`, `file_length`

**messages_fts** (FTS5)

- Virtual table for full-text search on `content`, `chat_jid`, `sender`, `timestamp`

### Environment Variables

- `DB_DIR` (default: `store`): Directory for SQLite databases and downloaded media
- `LOG_LEVEL` (default: `INFO`): Logging level (DEBUG, INFO, WARN, ERROR)
- `FFMPEG_PATH` (default: `ffmpeg`): Path to ffmpeg binary for audio conversion

### Storage Layout

```
store/
├── whatsapp.db           # whatsmeow session store
├── messages.db           # chats and messages SQLite database
└── <chatJID>/            # per-chat media downloads
    └── <filename>
```

## Key Implementation Details

### Recipients and Fuzzy Matching

The `send_message` tool supports three recipient formats:

- **Contact/Group Names**: `"John"`, `"Bob"`, `"Project Team"` - uses fuzzy search against chat history
- **Phone Numbers**: Without `+` (e.g., `447123456789`) - auto-converted to `@s.whatsapp.net` JID
- **Full JID**: `447123456789@s.whatsapp.net` for contacts, `123456@g.us` for groups - direct match

**Fuzzy Resolution Process (`ResolveRecipient` in resolver.go)**:
1. Check if input contains `@` → try parsing as JID
2. Check if input is all digits (5+ chars) → treat as phone number
3. Otherwise, search `chats` table with case-insensitive `LIKE` match on name field
4. If 0 matches → error with helpful message
5. If 1 match → return JID
6. If multiple matches → return error with disambiguation list showing all matches with their JIDs

### Media Sending

- `.ogg` files are sent directly as PTT (push-to-talk) with duration/waveform metadata
- Non-.ogg audio is converted via ffmpeg before sending
- Images/videos/documents are classified by extension and uploaded as appropriate message types

### FTS5 Requirement

- Build MUST include `-tags "sqlite_fts5"` and CGO_ENABLED=1
- Migration will fail with clear error if FTS5 is not available
- FTS5 enables `search_messages` tool to use `MATCH` queries instead of `LIKE`

### Name Resolution Priority

1. Existing DB cache (`chats.name`)
2. Conversation DisplayName/Name (from history sync)
3. Group info (`c.WA.GetGroupInfo`)
4. Contact info (`c.WA.Store.Contacts.GetContact`) – FullName → BusinessName → PushName
5. Sender phone/JID user part

### Event Handling

- `handleMessage`: Real-time incoming messages, upserts chat name and inserts message
- `handleHistorySync`: Bulk backfill from WhatsApp history, processes conversation arrays
- `backfillChatNames`: Post-connect job to update chats missing friendly names

## Prerequisites

- Go 1.24+
- macOS: Xcode Command Line Tools (`xcode-select --install`) for CGO
- ffmpeg (optional but recommended for audio conversion): `brew install ffmpeg`

## Testing First Run

1. Build: `make build`
2. Run: `./bin/whatsapp-mcp`
3. Scan QR code with WhatsApp mobile app (Settings → Linked Devices → Link a Device)
4. Wait for history sync to complete (log: "history sync persisted messages count=...")
5. Test MCP tools via Cursor/Claude by adding to `~/.cursor/mcp.json`

## Common Issues

- **"SQLite FTS5 is not available"**: Ensure CGO_ENABLED=1 and build with `-tags "sqlite_fts5"`
- **No messages appearing**: Wait for history sync after pairing; check logs for "history sync persisted messages"
- **Audio conversion fails**: Install ffmpeg or set FFMPEG_PATH environment variable
- **CGO errors on macOS**: Install Xcode Command Line Tools
