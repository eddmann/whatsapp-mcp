# WhatsApp MCP Server

A Model Context Protocol (MCP) server for WhatsApp integration. Send messages, search conversations, and access your WhatsApp history through Claude and other LLMs.

[![Go 1.24+](https://img.shields.io/badge/go-1.24+-blue.svg)](https://golang.org/dl/)

## Overview

This MCP server provides 6 tools to interact with your WhatsApp account via the whatsmeow library:

- Messaging (2 tools) - Send text and media messages to contacts and groups
- Chats (2 tools) - List conversations and retrieve message history
- Search (1 tool) - Full-text search across all messages using SQLite FTS5
- Media (1 tool) - Download and access media files from conversations

All messages and chats are persisted to a local SQLite database with full-text search capabilities, enabling rich queries and analysis of your WhatsApp history.

## Prerequisites

- Docker

## Installation & Setup

```bash
# Pull the image
docker pull ghcr.io/eddmann/whatsapp-mcp:latest
```

### First Run - WhatsApp Pairing

```bash
# Create directory for persistent storage
mkdir -p whatsapp-store

# Run the container (QR code will be displayed)
docker run -it --rm \
  -v "$(pwd)/whatsapp-store:/app/store" \
  ghcr.io/eddmann/whatsapp-mcp:latest
```

On first run, a QR code will be displayed in the terminal:

1. Open WhatsApp on your phone
2. Go to Settings → Linked Devices → Link a Device
3. Scan the QR code displayed in your terminal
4. Wait for history sync to complete (check logs for "history sync persisted messages count=...")

### Authentication & Data Storage

- Session Storage: WhatsApp session data is saved to `store/whatsapp.db` and persists across restarts
- Message Database: All messages and chats are stored in `store/messages.db` with FTS5 full-text search
- Media Downloads: Downloaded media files are organized in `store/<chatJID>/` directories
- No Re-Authentication: After initial pairing, the server automatically reconnects using stored credentials

> **Important:** Mount a volume to `/app/store` to persist session data and messages across container restarts.

## Claude Desktop Configuration

Add to your configuration file:

- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Windows: `%APPDATA%\Claude\claude_desktop_config.json`

### Basic Configuration

```json
{
  "mcpServers": {
    "whatsapp": {
      "command": "docker",
      "args": [
        "run",
        "-i",
        "--rm",
        "-v",
        "/ABSOLUTE/PATH/TO/whatsapp-store:/app/store",
        "ghcr.io/eddmann/whatsapp-mcp:latest"
      ]
    }
  }
}
```

### With Environment Variables

```json
{
  "mcpServers": {
    "whatsapp": {
      "command": "docker",
      "args": [
        "run",
        "-i",
        "--rm",
        "-v",
        "/ABSOLUTE/PATH/TO/whatsapp-store:/app/store",
        "-e",
        "LOG_LEVEL=DEBUG",
        "ghcr.io/eddmann/whatsapp-mcp:latest"
      ]
    }
  }
}
```

### Available Environment Variables

- `LOG_LEVEL` - Logging level (DEBUG, INFO, WARN, ERROR) - default: `INFO`

## Usage

Ask Claude to interact with your WhatsApp data using natural language.

### Sending Messages

```
"Send a message to John saying I'll be there in 10 minutes"
"Send a photo from ~/Desktop/photo.jpg to Mom"
"Send this audio recording to the Project Team group"
```

> **Recipient Formats:**
>
> - Direct messages: Phone number without `+` (e.g., `441234567890`)
> - Groups: Full JID (e.g., `123456789@g.us`) or group name if already in database

### Viewing Conversations

```
"Show me my recent chats"
"List my last 20 messages with Sarah"
"Show the conversation with Dad from yesterday with context"
```

### Searching Messages

```
"Search for all messages mentioning 'project deadline'"
"Find messages from Alice about the meeting"
"Search for 'birthday party' in my group chats"
```

### Media Handling

```
"Download the latest photo from the Family group"
"Save that video Mike sent me yesterday"
```

## Available Tools

| Tool              | Description                                                        |
| ----------------- | ------------------------------------------------------------------ |
| `list_chats`      | List all WhatsApp conversations with last message time             |
| `list_messages`   | Retrieve message history for a specific chat with optional context |
| `search_messages` | Full-text search across all messages using FTS5                    |
| `send_text`       | Send a text message to a contact or group                          |
| `send_media`      | Send media (image, video, audio, document) with optional caption   |
| `download_media`  | Download media files from messages to local storage                |

## Architecture

### Core Components

- WhatsApp Client (`internal/wa/client.go`) - Wraps whatsmeow for connection management, event handling, and message operations
- SQLite Store (`internal/store/store.go`) - Persists messages and chats with FTS5 full-text search
- Media Processing (`internal/media/`) - Handles audio conversion (ffmpeg) and Opus metadata extraction
- MCP Server (`cmd/whatsapp-mcp/main.go`) - Exposes tools over stdio using mark3labs/mcp-go

### Data Flow

1. Message Reception: whatsmeow events → event handlers → SQLite (chats + messages + FTS5)
2. Name Resolution: Database cache → conversation metadata → group info → contacts → fallback to JID
3. Sending: Parse recipient → classify media → upload → construct proto message → send
4. Audio: Non-.ogg files are converted to Opus via ffmpeg before sending as PTT (push-to-talk)

### Database Schema

`chats` table:

- `jid` (PK): WhatsApp JID identifier
- `name`: Human-friendly name (resolved from contacts/groups)
- `last_message_time`: Timestamp of latest message

`messages` table:

- `(id, chat_jid)` (PK): Unique message ID per chat
- `sender`: Phone number or JID of sender
- `content`: Text content or emoji summary for media
- `timestamp`: Message timestamp
- `is_from_me`: Boolean indicating sent by you
- Media fields: `media_type`, `filename`, `url`, `media_key`, `file_sha256`, etc.

`messages_fts` (FTS5):

- Virtual table for full-text search on `content`, `chat_jid`, `sender`, `timestamp`

## Key Features

### Full-Text Search with FTS5

Messages are indexed using SQLite's FTS5 extension, enabling fast and powerful search queries:

```
"Search for messages containing 'vacation OR holiday' from last month"
"Find all messages from Sarah about the project"
```

### Automatic Audio Conversion

Non-.ogg audio files are automatically converted to Opus format using ffmpeg before sending:

- Converts to 32kbps, 24kHz Opus in Ogg container
- Extracts duration and waveform metadata for WhatsApp PTT
- Supports all ffmpeg-compatible audio formats

### Context-Aware Message Retrieval

The `list_messages` tool can include surrounding messages for context:

```
"Show me the conversation with context around the message about dinner"
```

### Intelligent Name Resolution

Contact and group names are resolved with multiple fallback strategies:

1. Cached database name
2. Conversation display name
3. WhatsApp group info
4. Contact info (`FullName` → `BusinessName` → `PushName`)
5. Phone number/JID as fallback

## Storage Layout

```
store/
├── whatsapp.db           # whatsmeow session store
├── messages.db           # chats and messages SQLite database
└── <chatJID>/            # per-chat media downloads
    └── <filename>
```

## Common Issues

### No messages appearing after pairing

Wait for the initial history sync to complete. Check logs for:

```
history sync persisted messages count=...
```

> **Note:** History sync can take several minutes depending on the size of your WhatsApp history.

## License

MIT License - see [LICENSE](LICENSE) file for details

## Disclaimer

This project is not affiliated with, endorsed by, or sponsored by WhatsApp Inc. or Meta Platforms, Inc. Use at your own risk. Ensure compliance with WhatsApp's Terms of Service when using this software.

## Credits

Inspired by [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp)
