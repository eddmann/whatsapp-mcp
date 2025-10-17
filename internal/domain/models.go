package domain

import "time"

// Chat represents a WhatsApp chat (direct message or group).
type Chat struct {
	JID             string     `json:"jid"`
	Name            *string    `json:"name,omitempty"`
	IsGroup         bool       `json:"is_group"`
	LastMessageTime *time.Time `json:"last_message_time,omitempty"`
	LastMessage     *string    `json:"last_message,omitempty"`
	LastSender      *string    `json:"last_sender,omitempty"`
	LastIsFromMe    *bool      `json:"last_is_from_me,omitempty"`
}

// Message represents a WhatsApp message.
type Message struct {
	ID          string     `json:"id"`
	ChatJID     string     `json:"chat_jid"`
	Sender      string     `json:"sender"`
	Content     *string    `json:"content,omitempty"`
	Timestamp   time.Time  `json:"timestamp"`
	IsFromMe    bool       `json:"is_from_me"`
	MediaType   *string    `json:"media_type,omitempty"`
	Filename    *string    `json:"filename,omitempty"`
	ChatName    *string    `json:"chat_name,omitempty"`
}

// MessageContext represents a message with surrounding context.
type MessageContext struct {
	Message Message   `json:"message"`
	Before  []Message `json:"before"`
	After   []Message `json:"after"`
}

// Contact represents a WhatsApp contact (non-group).
type Contact struct {
	JID   string  `json:"jid"`
	Phone string  `json:"phone_number"`
	Name  *string `json:"name,omitempty"`
}

// SendResult represents the result of sending a message.
type SendResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// DownloadResult represents the result of downloading media.
type DownloadResult struct {
	Success  bool   `json:"success"`
	Message  string `json:"message"`
	Filename string `json:"filename,omitempty"`
	Path     string `json:"path,omitempty"`
}

// ListChatsOptions contains options for listing chats.
type ListChatsOptions struct {
	Query              string
	Limit              int
	Page               int
	SortBy             string
	IncludeLastMessage bool
}

// ListMessagesOptions contains options for listing messages.
type ListMessagesOptions struct {
	After          string
	Before         string
	Sender         string
	ChatJID        string
	Query          string
	Limit          int
	Page           int
	IncludeContext bool
	ContextBefore  int
	ContextAfter   int
}

// SearchMessagesOptions contains options for searching messages.
type SearchMessagesOptions struct {
	Query string
	Limit int
	Page  int
}
