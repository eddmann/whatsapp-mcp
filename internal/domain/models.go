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
	ID        string    `json:"id"`
	ChatJID   string    `json:"chat_jid"`
	Sender    string    `json:"sender"`
	Content   *string   `json:"content,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	IsFromMe  bool      `json:"is_from_me"`
	MediaType *string   `json:"media_type,omitempty"`
	Filename  *string   `json:"filename,omitempty"`
	ChatName  *string   `json:"chat_name,omitempty"`
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
	Success   bool    `json:"success"`
	Message   string  `json:"message"`
	MessageID *string `json:"message_id,omitempty"`
	ChatJID   *string `json:"chat_jid,omitempty"`
	Timestamp *string `json:"timestamp,omitempty"`
}

// DownloadResult represents the result of downloading media.
type DownloadResult struct {
	Success  bool   `json:"success"`
	Message  string `json:"message"`
	Filename string `json:"filename,omitempty"`
	Path     string `json:"path,omitempty"`
}

// ListChatsOptions contains options for listing chats.
// Always sorted by last activity and includes last message preview.
type ListChatsOptions struct {
	Query      string
	OnlyGroups bool
	Limit      int
	Page       int
}

// ListMessagesOptions contains options for listing messages.
type ListMessagesOptions struct {
	After     string
	Before    string
	Timeframe string // Natural time range: "today", "yesterday", "this_week", etc.
	ChatJID   string
	Limit     int
	Page      int
}

// SearchMessagesOptions contains options for searching messages.
// Always includes ±2 surrounding messages for context.
type SearchMessagesOptions struct {
	Query     string
	After     string
	Before    string
	Timeframe string // Natural time range: "today", "yesterday", "this_week", etc.
	Limit     int
	Page      int
}

// CatchUpOptions contains options for the catch_up composite tool.
// Always includes media summary with standard detail level.
type CatchUpOptions struct {
	Timeframe  string // Natural time range: "last_hour", "today", "yesterday", etc.
	OnlyGroups bool   // Only include group chat activity
}

// CatchUpSummary represents the result of a catch_up operation.
type CatchUpSummary struct {
	Timeframe      string           `json:"timeframe"`
	Summary        string           `json:"summary"`
	TotalMessages  int              `json:"total_messages"`
	ActiveChats    []ActiveChatInfo `json:"active_chats"`
	QuestionsForMe []Message        `json:"questions_for_me,omitempty"`
	MediaSummary   *MediaSummary    `json:"media_summary,omitempty"`
	NeedsAttention []string         `json:"needs_attention,omitempty"` // Chat names with unanswered questions
}

// ActiveChatInfo represents an active chat with recent activity.
type ActiveChatInfo struct {
	ChatJID         string    `json:"chat_jid"`
	ChatName        string    `json:"chat_name"`
	IsGroup         bool      `json:"is_group"`
	MessageCount    int       `json:"message_count"`
	LastMessageTime time.Time `json:"last_message_time"`
	LastMessageText *string   `json:"last_message_text,omitempty"`
	LastIsFromMe    bool      `json:"last_is_from_me"`
	HasQuestions    bool      `json:"has_questions"`
	RecentMessages  []Message `json:"recent_messages,omitempty"`
}

// MediaSummary represents media activity in a timeframe.
type MediaSummary struct {
	PhotoCount    int      `json:"photo_count"`
	VideoCount    int      `json:"video_count"`
	AudioCount    int      `json:"audio_count"`
	DocumentCount int      `json:"document_count"`
	FromChats     []string `json:"from_chats,omitempty"` // Chat names with media
}
