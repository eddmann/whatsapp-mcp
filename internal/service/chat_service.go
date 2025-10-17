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
	// Validation
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

// SearchContacts searches for contacts by name or JID.
func (s *ChatService) SearchContacts(query string) ([]domain.Contact, error) {
	if query == "" {
		return nil, fmt.Errorf("query cannot be empty")
	}

	return s.store.SearchContacts(query)
}

// GetChat retrieves a single chat by JID.
func (s *ChatService) GetChat(chatJID string, includeLast bool) (*domain.Chat, error) {
	if chatJID == "" {
		return nil, fmt.Errorf("chat_jid cannot be empty")
	}

	return s.store.GetChat(chatJID, includeLast)
}

// GetDirectChatByContact retrieves a direct chat by phone number.
func (s *ChatService) GetDirectChatByContact(phone string) (*domain.Chat, error) {
	if phone == "" {
		return nil, fmt.Errorf("phone cannot be empty")
	}

	return s.store.GetDirectChatByContact(phone)
}

// GetContactChats retrieves chats involving a contact.
func (s *ChatService) GetContactChats(jid string, limit, page int) ([]domain.Chat, error) {
	if jid == "" {
		return nil, fmt.Errorf("jid cannot be empty")
	}

	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		return nil, fmt.Errorf("limit cannot exceed 200")
	}
	if page < 0 {
		page = 0
	}

	return s.store.GetContactChats(jid, limit, page)
}
