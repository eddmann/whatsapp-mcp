package store

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

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
