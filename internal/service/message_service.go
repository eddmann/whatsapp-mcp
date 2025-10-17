package service

import (
	"fmt"

	"github.com/eddmann/whatsapp-mcp/internal/domain"
	"github.com/eddmann/whatsapp-mcp/internal/store"
	"github.com/eddmann/whatsapp-mcp/internal/wa"
)

// MessageService handles message-related business logic.
type MessageService struct {
	store  *store.DB
	client *wa.Client
}

// NewMessageService creates a new MessageService.
func NewMessageService(store *store.DB, client *wa.Client) *MessageService {
	return &MessageService{
		store:  store,
		client: client,
	}
}

// ListMessages lists messages with filters and pagination.
func (s *MessageService) ListMessages(opts domain.ListMessagesOptions) ([]domain.Message, error) {
	// Validation
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	if opts.Limit > 200 {
		return nil, fmt.Errorf("limit cannot exceed 200")
	}
	if opts.Page < 0 {
		opts.Page = 0
	}
	if opts.ContextBefore < 0 {
		opts.ContextBefore = 0
	}
	if opts.ContextAfter < 0 {
		opts.ContextAfter = 0
	}

	return s.store.ListMessages(opts)
}

// GetMessageContext retrieves messages before and after a specific message.
func (s *MessageService) GetMessageContext(messageID string, before, after int) (*domain.MessageContext, error) {
	if messageID == "" {
		return nil, fmt.Errorf("message_id cannot be empty")
	}

	if before <= 0 {
		before = 5
	}
	if after <= 0 {
		after = 5
	}
	if before > 100 {
		return nil, fmt.Errorf("before cannot exceed 100")
	}
	if after > 100 {
		return nil, fmt.Errorf("after cannot exceed 100")
	}

	return s.store.GetMessageContext(messageID, before, after)
}

// GetLastInteraction retrieves the most recent message involving a contact.
func (s *MessageService) GetLastInteraction(jid string) (*domain.Message, error) {
	if jid == "" {
		return nil, fmt.Errorf("jid cannot be empty")
	}

	return s.store.GetLastInteraction(jid)
}

// SearchMessages performs full-text search on message content.
func (s *MessageService) SearchMessages(opts domain.SearchMessagesOptions) ([]domain.Message, error) {
	if opts.Query == "" {
		return nil, fmt.Errorf("query cannot be empty")
	}

	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	if opts.Limit > 200 {
		return nil, fmt.Errorf("limit cannot exceed 200")
	}
	if opts.Page < 0 {
		opts.Page = 0
	}

	return s.store.SearchMessages(opts)
}

// SendText sends a text message to a recipient.
func (s *MessageService) SendText(recipient, message, replyToMessageID string) (*domain.SendResult, error) {
	if recipient == "" {
		return nil, fmt.Errorf("recipient cannot be empty")
	}
	if message == "" {
		return nil, fmt.Errorf("message cannot be empty")
	}

	result, err := s.client.SendText(recipient, message, replyToMessageID)
	if err != nil {
		return &domain.SendResult{Success: false, Message: err.Error()}, nil
	}

	return &domain.SendResult{
		Success:   result.Success,
		Message:   result.Message,
		MessageID: ptrIfNotEmpty(result.MessageID),
		ChatJID:   ptrIfNotEmpty(result.ChatJID),
		Timestamp: ptrIfNotEmpty(result.Timestamp),
	}, nil
}

// SendMedia sends a media file to a recipient with optional caption.
func (s *MessageService) SendMedia(recipient, mediaPath, caption, replyToMessageID string) (*domain.SendResult, error) {
	if recipient == "" {
		return nil, fmt.Errorf("recipient cannot be empty")
	}
	if mediaPath == "" {
		return nil, fmt.Errorf("media_path cannot be empty")
	}

	result, err := s.client.SendMedia(recipient, mediaPath, caption, replyToMessageID)
	if err != nil {
		return &domain.SendResult{Success: false, Message: err.Error()}, nil
	}

	return &domain.SendResult{
		Success:   result.Success,
		Message:   result.Message,
		MessageID: ptrIfNotEmpty(result.MessageID),
		ChatJID:   ptrIfNotEmpty(result.ChatJID),
		Timestamp: ptrIfNotEmpty(result.Timestamp),
	}, nil
}

// DownloadMedia downloads media from a message.
func (s *MessageService) DownloadMedia(messageID, chatJID string) (*domain.DownloadResult, error) {
	if messageID == "" {
		return nil, fmt.Errorf("message_id cannot be empty")
	}
	if chatJID == "" {
		return nil, fmt.Errorf("chat_jid cannot be empty")
	}

	result, err := s.client.DownloadMedia(messageID, chatJID)
	if err != nil {
		return &domain.DownloadResult{Success: false, Message: err.Error()}, nil
	}

	return &domain.DownloadResult{
		Success:  result.Success,
		Message:  fmt.Sprintf("downloaded %s", result.MediaType),
		Filename: result.Filename,
		Path:     result.Path,
	}, nil
}

// ptrIfNotEmpty returns a pointer to the string if it's not empty, otherwise nil.
func ptrIfNotEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
