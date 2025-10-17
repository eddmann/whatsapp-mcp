package wa

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"

	"go.mau.fi/whatsmeow/types"
)

// getChatName attempts to resolve a friendly chat name using existing DB,
// conversation metadata, group info, or contacts.
func (c *Client) getChatName(jid types.JID, chatJID string, conversation any, sender string) string {
	// Existing stored name
	var existing sql.NullString
	_ = c.Store.Messages.QueryRow("SELECT name FROM chats WHERE jid = ?", chatJID).Scan(&existing)
	if existing.Valid && existing.String != "" {
		return existing.String
	}

	// Try to extract from conversation (DisplayName or Name), using reflection to avoid tight coupling
	if conversation != nil {
		v := reflect.ValueOf(conversation)
		if v.Kind() == reflect.Ptr && !v.IsNil() {
			v = v.Elem()
		}
		if v.IsValid() {
			if f := v.FieldByName("DisplayName"); f.IsValid() && f.Kind() == reflect.Ptr && !f.IsNil() {
				if dn, ok := f.Elem().Interface().(string); ok && dn != "" {
					return dn
				}
			}
			if f := v.FieldByName("Name"); f.IsValid() && f.Kind() == reflect.Ptr && !f.IsNil() {
				if n, ok := f.Elem().Interface().(string); ok && n != "" {
					return n
				}
			}
		}
	}

	// Groups
	if jid.Server == "g.us" {
		if info, err := c.WA.GetGroupInfo(jid); err == nil && info.Name != "" {
			return info.Name
		}
		return fmt.Sprintf("Group %s", jid.User)
	}

	// Contacts
	if contact, err := c.WA.Store.Contacts.GetContact(context.Background(), jid); err == nil {
		if contact.FullName != "" {
			return contact.FullName
		}
		if contact.BusinessName != "" {
			return contact.BusinessName
		}
		if contact.PushName != "" {
			return contact.PushName
		}
	}

	if sender != "" {
		return sender
	}
	return jid.User
}

// resolvePreferredName tries to resolve a human-friendly name for a JID using
// live WA data only (contacts/groups), ignoring any cached DB value. This is
// used by backfill to improve chats that only have phone numbers stored.
func (c *Client) resolvePreferredName(jid types.JID) string {
	// Groups
	if jid.Server == "g.us" {
		if info, err := c.WA.GetGroupInfo(jid); err == nil && info.Name != "" {
			return info.Name
		}
		return fmt.Sprintf("Group %s", jid.User)
	}

	// Contacts
	if contact, err := c.WA.Store.Contacts.GetContact(context.Background(), jid); err == nil {
		if contact.FullName != "" {
			return contact.FullName
		}
		if contact.BusinessName != "" {
			return contact.BusinessName
		}
		if contact.PushName != "" {
			return contact.PushName
		}
	}

	return jid.User
}

// backfillChatNames finds chats without a proper name and updates them using
// contact/group information once available post-connect.
func (c *Client) backfillChatNames() {
	if c.Store == nil || c.Store.Messages == nil {
		return
	}

	rows, err := c.Store.Messages.Query(`SELECT jid, COALESCE(name, '') FROM chats`)
	if err != nil {
		c.Logger.Warn("backfill: query chats failed", "err", err)
		return
	}
	defer rows.Close()

	type row struct {
		jid  string
		name string
	}
	var toUpdate []row

	for rows.Next() {
		var jidStr, name string
		if err := rows.Scan(&jidStr, &name); err != nil {
			c.Logger.Warn("backfill: scan failed", "err", err)
			continue
		}

		// Skip groups here; resolvePreferredName handles them but they usually already have names
		parsed, err := types.ParseJID(jidStr)
		if err != nil {
			continue
		}

		if parsed.Server == "g.us" {
			// update groups if missing a name
			if name == "" || name == parsed.User {
				toUpdate = append(toUpdate, row{jid: jidStr, name: name})
			}
			continue
		}

		// For individual chats: consider missing or numeric-like names as needing backfill
		phone := parsed.User
		if name == "" || name == phone || strings.HasSuffix(name, "@s.whatsapp.net") {
			toUpdate = append(toUpdate, row{jid: jidStr, name: name})
		}
	}

	if err := rows.Err(); err != nil {
		c.Logger.Warn("backfill: rows error", "err", err)
	}

	updated := 0
	for _, r := range toUpdate {
		parsed, err := types.ParseJID(r.jid)
		if err != nil {
			continue
		}

		resolved := c.resolvePreferredName(parsed)
		if resolved == "" || resolved == parsed.User || resolved == r.name {
			continue
		}

		if _, err := c.Store.Messages.Exec(`UPDATE chats SET name = ? WHERE jid = ?`, resolved, r.jid); err != nil {
			c.Logger.Warn("backfill: update failed", "jid", r.jid, "err", err)
			continue
		}
		updated++
	}

	if updated > 0 {
		c.Logger.Info("backfill: updated chat names", "count", updated)
	}
}
