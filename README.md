# WhatsApp MCP Server

![WhatsApp MCP Server](docs/heading.png)

A Model Context Protocol (MCP) server for WhatsApp integration. Send messages, search conversations, and access your WhatsApp history through Claude and other LLMs.

[![Go 1.24+](https://img.shields.io/badge/go-1.24+-blue.svg)](https://golang.org/dl/)

## Overview

This MCP server provides 7 tools to interact with your WhatsApp account via the whatsmeow library:

- **list_chats** - List conversations with filtering, sorting, and pagination
- **list_messages** - Retrieve message history with date range filtering and context
- **search_messages** - Full-text search across all messages using SQLite FTS5 with context
- **send_message** - Send text and media messages with fuzzy name matching and reply/threading
- **download_media** - Download media files from conversations to local storage
- **get_connection_status** - Check WhatsApp connection status and database statistics
- **catch_up** - Intelligent activity summary showing recent chats, questions, and media

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

- `DB_DIR` - Directory for SQLite databases and downloaded media - default: `store`
- `LOG_LEVEL` - Logging level (DEBUG, INFO, WARN, ERROR) - default: `INFO`
- `FFMPEG_PATH` - Path to ffmpeg binary for audio conversion - default: `ffmpeg`

## Usage

Ask Claude to interact with your WhatsApp data using natural language.

### Sending Messages

```
"Send a message to John saying I'll be there in 10 minutes"
"Send a photo from ~/Desktop/photo.jpg to Bob"
"Send this audio recording to the Project Team group"
"Reply to that message from Sarah saying 'Sounds good!'"
```

> **Recipient Formats (with fuzzy name matching):**
>
> - Contact/group names: `"John"`, `"Bob"`, `"Project Team"` (searches your chat history)
> - Phone numbers: Without `+` (e.g., `447123456789`)
> - Full JID: `447123456789@s.whatsapp.net` for contacts, `123456@g.us` for groups
>
> If multiple matches are found for a name, you'll be prompted to disambiguate using the full JID.

> **Message Threading:**
>
> Reply to specific messages to create threaded conversations. The original message will be quoted in your reply.

### Viewing Conversations

```
"Show me my recent chats"
"List my last 20 messages with John"
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
"Save that video Mick sent me yesterday"
```

### Getting Activity Summaries

```
"Catch me up on today's WhatsApp activity"
"Show me what happened in my WhatsApp groups this week"
"What questions have I been asked today?"
```

## Available Tools

| Tool                    | Description                                                                                                                             |
| ----------------------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| `list_chats`            | List conversations with message previews, sorted by recent activity. Filter by name/phone/groups-only. Supports pagination.             |
| `list_messages`         | List messages from a conversation. Filter by contact/group name and date range using natural timeframes (today, this_week, etc).        |
| `search_messages`       | Full-text search with FTS5 across all messages. Supports keywords, phrases, boolean operators, and date filters.                        |
| `send_message`          | Send text, media (image/video/audio/document), or both to contacts/groups. Fuzzy name matching and message reply/threading support.     |
| `download_media`        | Download media files (image/video/audio/document) from messages to local storage organized by chat.                                     |
| `get_connection_status` | Check WhatsApp connection status, login state, device info, and database statistics (chat and message counts).                          |
| `catch_up`              | Intelligent activity summary showing active chats with recent messages, questions directed at you, media activity, and attention flags. |

## License

MIT License - see [LICENSE](LICENSE) file for details

## Disclaimer

This project is not affiliated with, endorsed by, or sponsored by WhatsApp Inc. or Meta Platforms, Inc. Use at your own risk. Ensure compliance with WhatsApp's Terms of Service when using this software.

## Credits

Inspired by [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp)
