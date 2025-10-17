package wa

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/eddmann/whatsapp-mcp/internal/store"
)

// Client wraps a WhatsApp client with store integration and logging.
type Client struct {
	WA      *whatsmeow.Client
	Store   *store.DB
	Logger  *slog.Logger
	BaseDir string
}

// New creates a new WhatsApp client with the given store and configuration.
func New(db *store.DB, baseDir string, logLevel string, appLogger *slog.Logger) (*Client, error) {
	if baseDir == "" {
		baseDir = "store"
	}
	if logLevel == "" {
		logLevel = "INFO"
	}

	// Normalize log level
	lvl := strings.ToUpper(logLevel)
	var zerologLevel zerolog.Level
	switch lvl {
	case "DEBUG":
		zerologLevel = zerolog.DebugLevel
	case "INFO":
		zerologLevel = zerolog.InfoLevel
	case "WARN":
		zerologLevel = zerolog.WarnLevel
	case "ERROR":
		zerologLevel = zerolog.ErrorLevel
	default:
		zerologLevel = zerolog.InfoLevel
	}

	// Create zerolog loggers that write to stderr
	waZerolog := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"}).
		Level(zerologLevel).
		With().
		Timestamp().
		Str("module", "wa").
		Logger()
	dbZerolog := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"}).
		Level(zerologLevel).
		With().
		Timestamp().
		Str("module", "wa-db").
		Logger()

	// Wrap with waLog
	waLogger := waLog.Zerolog(waZerolog)
	dbLog := waLog.Zerolog(dbZerolog)

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
