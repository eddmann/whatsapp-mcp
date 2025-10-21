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
	var existing sql.NullString
	_ = c.Store.Messages.QueryRow("SELECT name FROM chats WHERE jid = ?", chatJID).Scan(&existing)
	if existing.Valid && existing.String != "" {
		return existing.String
	}

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

	if jid.Server == "g.us" {
		if info, err := c.WA.GetGroupInfo(jid); err == nil && info.Name != "" {
			return info.Name
		}
		return fmt.Sprintf("Group %s", jid.User)
	}

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

// ResolveRecipient attempts to resolve a recipient string (phone, JID, or name) to a WhatsApp JID.
// Returns the resolved JID string, or an error if not found or ambiguous.
func (c *Client) ResolveRecipient(recipient string) (string, error) {
	if recipient == "" {
		return "", fmt.Errorf("recipient cannot be empty")
	}

	if strings.Contains(recipient, "@") {
		jid, err := types.ParseJID(recipient)
		if err == nil {
			return jid.String(), nil
		}
	}

	isPhone := true
	for _, ch := range recipient {
		if ch < '0' || ch > '9' {
			isPhone = false
			break
		}
	}
	if isPhone && len(recipient) > 5 {
		return fmt.Sprintf("%s@s.whatsapp.net", recipient), nil
	}

	pattern := "%" + strings.ToLower(recipient) + "%"
	rows, err := c.Store.Messages.Query(`
		SELECT jid, name FROM chats
		WHERE LOWER(name) LIKE ?
		ORDER BY name LIMIT 10`, pattern)
	if err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}
	defer rows.Close()

	type match struct {
		jid  string
		name string
	}
	var matches []match

	for rows.Next() {
		var jid string
		var name sql.NullString
		if err := rows.Scan(&jid, &name); err != nil {
			continue
		}
		matches = append(matches, match{jid: jid, name: name.String})
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("no contact or group found matching '%s'. Use phone number (e.g., 441234567890) or full JID (e.g., 123456@g.us)", recipient)
	}

	if len(matches) == 1 {
		return matches[0].jid, nil
	}

	var suggestions []string
	for _, m := range matches {
		if m.name != "" {
			suggestions = append(suggestions, fmt.Sprintf("%s (%s)", m.name, m.jid))
		} else {
			suggestions = append(suggestions, m.jid)
		}
	}
	return "", fmt.Errorf("multiple matches found for '%s': %s. Please use the full JID to disambiguate", recipient, strings.Join(suggestions, ", "))
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

		parsed, err := types.ParseJID(jidStr)
		if err != nil {
			continue
		}

		if parsed.Server == "g.us" {
			if name == "" || name == parsed.User {
				toUpdate = append(toUpdate, row{jid: jidStr, name: name})
			}
			continue
		}

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
