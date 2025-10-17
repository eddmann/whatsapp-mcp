package wa

import (
	"context"
	"os"

	"github.com/mdp/qrterminal"
	"go.mau.fi/whatsmeow/types/events"
)

// registerHandlers registers event handlers for WhatsApp events.
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

// ConnectWithQR connects to WhatsApp, displaying a QR code if needed.
func (c *Client) ConnectWithQR(ctx context.Context) error {
	if c.WA.Store.ID == nil {
		qrChan, _ := c.WA.GetQRChannel(ctx)
		if err := c.WA.Connect(); err != nil {
			return err
		}

		for evt := range qrChan {
			if evt.Event == "code" {
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stderr)
			} else if evt.Event == "success" {
				break
			}
		}

		return nil
	}

	return c.WA.Connect()
}
