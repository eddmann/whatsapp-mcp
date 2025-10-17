# WhatsApp MCP Server

A Model Context Protocol (MCP) server for WhatsApp integration. Send messages, search conversations, and access your WhatsApp history through Claude and other LLMs.

[![Go 1.24+](https://img.shields.io/badge/go-1.24+-blue.svg)](https://golang.org/dl/)

## Overview

This MCP server provides 14 tools to interact with your WhatsApp account via the whatsmeow library:

- **Messaging (1 tool)** - Send text and media messages (with fuzzy name matching)
- **Chats (5 tools)** - List, search, and manage conversations and contacts
- **Messages (4 tools)** - Retrieve message history with context and filtering
- **Search (1 tool)** - Full-text search across all messages using SQLite FTS5
- **Media (1 tool)** - Download and access media files from conversations
- **Status (1 tool)** - Check connection status and server health

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
```

> **Recipient Formats (with fuzzy name matching):**
>
> - Contact/group names: `"John"`, `"Bob"`, `"Project Team"` (searches your chat history)
> - Phone numbers: Without `+` (e.g., `447123456789`)
> - Full JID: `447123456789@s.whatsapp.net` for contacts, `123456@g.us` for groups
>
> If multiple matches are found for a name, you'll be prompted to disambiguate using the full JID.

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

## Available Tools

### Chat Management

| Tool                         | Description                                                         |
| ---------------------------- | ------------------------------------------------------------------- |
| `list_chats`                 | List WhatsApp conversations with filtering, sorting, and pagination |
| `get_chat`                   | Get detailed information about a specific chat by JID               |
| `search_contacts`            | Search for contacts by name or phone number                         |
| `get_direct_chat_by_contact` | Get direct message chat by phone number                             |
| `get_contact_chats`          | List all chats (DMs and groups) involving a specific contact        |

### Message Operations

| Tool                   | Description                                                                 |
| ---------------------- | --------------------------------------------------------------------------- |
| `list_messages`        | List messages with powerful filtering (date range, sender, chat, content)   |
| `get_message_context`  | Get surrounding messages around a specific message for conversation context |
| `get_last_interaction` | Get the most recent message with a specific contact                         |
| `search_messages`      | Full-text search across all messages using FTS5 with advanced query syntax  |

### Messaging

| Tool           | Description                                                                 |
| -------------- | --------------------------------------------------------------------------- |
| `send_message` | Send text, media, or both to contacts/groups (supports fuzzy name matching) |

### Media

| Tool             | Description                                                                      |
| ---------------- | -------------------------------------------------------------------------------- |
| `download_media` | Download media files (image/video/audio/document) from messages to local storage |

### Status

| Tool                    | Description                                                            |
| ----------------------- | ---------------------------------------------------------------------- |
| `get_connection_status` | Check WhatsApp connection status, login state, and database statistics |

## License

MIT License - see [LICENSE](LICENSE) file for details

## Disclaimer

This project is not affiliated with, endorsed by, or sponsored by WhatsApp Inc. or Meta Platforms, Inc. Use at your own risk. Ensure compliance with WhatsApp's Terms of Service when using this software.

## Credits

Inspired by [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp)
