package service

import (
	"fmt"

	"github.com/eddmann/whatsapp-mcp/internal/domain"
	"github.com/eddmann/whatsapp-mcp/internal/store"
)

// ChatService handles chat-related business logic.
type ChatService struct {
	store *store.DB
}

// NewChatService creates a new ChatService.
func NewChatService(store *store.DB) *ChatService {
	return &ChatService{store: store}
}

// ListChats lists chats with optional filtering, pagination and sorting.
func (s *ChatService) ListChats(opts domain.ListChatsOptions) ([]domain.Chat, error) {
	if opts.Limit > 200 {
		return nil, fmt.Errorf("limit cannot exceed 200")
	}
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	if opts.Page < 0 {
		opts.Page = 0
	}

	return s.store.ListChats(opts)
}

// GetChat retrieves a single chat by JID.
func (s *ChatService) GetChat(chatJID string, includeLast bool) (*domain.Chat, error) {
	if chatJID == "" {
		return nil, fmt.Errorf("chat_jid cannot be empty")
	}

	return s.store.GetChat(chatJID, includeLast)
}
