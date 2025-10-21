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
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	if opts.Limit > 200 {
		return nil, fmt.Errorf("limit cannot exceed 200")
	}
	if opts.Page < 0 {
		opts.Page = 0
	}

	if opts.Timeframe != "" {
		if opts.After != "" || opts.Before != "" {
			return nil, fmt.Errorf("cannot specify both timeframe and after/before parameters")
		}
		after, before, err := domain.ParseTimeframe(opts.Timeframe)
		if err != nil {
			return nil, fmt.Errorf("invalid timeframe: %w", err)
		}
		opts.After = after
		opts.Before = before
	}

	return s.store.ListMessages(opts)
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

	if opts.Timeframe != "" {
		if opts.After != "" || opts.Before != "" {
			return nil, fmt.Errorf("cannot specify both timeframe and after/before parameters")
		}
		after, before, err := domain.ParseTimeframe(opts.Timeframe)
		if err != nil {
			return nil, fmt.Errorf("invalid timeframe: %w", err)
		}
		opts.After = after
		opts.Before = before
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

// CatchUp provides an intelligent summary of recent WhatsApp activity.
// Uses standard detail level: up to 10 active chats with 3 recent messages each,
// and up to 10 questions directed at the user.
func (s *MessageService) CatchUp(opts domain.CatchUpOptions) (*domain.CatchUpSummary, error) {
	if opts.Timeframe == "" {
		opts.Timeframe = "today"
	}

	const (
		maxActiveChats   = 10
		maxRecentPerChat = 3
		maxQuestions     = 10
	)

	after, before, err := domain.ParseTimeframe(opts.Timeframe)
	if err != nil {
		return nil, fmt.Errorf("invalid timeframe: %w", err)
	}

	summary := &domain.CatchUpSummary{
		Timeframe: opts.Timeframe,
	}

	var totalCount int
	query := "SELECT COUNT(*) FROM messages WHERE datetime(timestamp) > datetime(?) AND datetime(timestamp) < datetime(?)"
	s.store.Messages.QueryRow(query, after, before).Scan(&totalCount)
	summary.TotalMessages = totalCount

	activeChats, err := s.store.GetActiveChats(after, before, opts.OnlyGroups, maxActiveChats)
	if err == nil {
		if maxRecentPerChat > 0 {
			for i := range activeChats {
				recentMsgs, err := s.store.ListMessages(domain.ListMessagesOptions{
					ChatJID: activeChats[i].ChatJID,
					After:   after,
					Before:  before,
					Limit:   maxRecentPerChat,
				})
				if err == nil {
					activeChats[i].RecentMessages = recentMsgs
				}
			}
		}
		summary.ActiveChats = activeChats
	}

	if maxQuestions > 0 {
		questions, err := s.store.GetQuestionsForMe(after, before, maxQuestions)
		if err == nil && len(questions) > 0 {
			summary.QuestionsForMe = questions

			needsAttention := make(map[string]bool)
			for _, q := range questions {
				if q.ChatName != nil {
					needsAttention[*q.ChatName] = true
				}
			}
			for chatName := range needsAttention {
				summary.NeedsAttention = append(summary.NeedsAttention, chatName)
			}
		}
	}

	mediaSummary, err := s.store.GetMediaSummary(after, before)
	if err == nil {
		summary.MediaSummary = mediaSummary
	}

	summary.Summary = s.generateCatchUpSummary(summary)

	return summary, nil
}

// generateCatchUpSummary creates a natural language summary.
func (s *MessageService) generateCatchUpSummary(data *domain.CatchUpSummary) string {
	if data.TotalMessages == 0 {
		return fmt.Sprintf("No messages in the last %s.", data.Timeframe)
	}

	summary := fmt.Sprintf("%d messages across %d chats", data.TotalMessages, len(data.ActiveChats))

	if len(data.QuestionsForMe) > 0 {
		summary += fmt.Sprintf(", including %d questions directed at you", len(data.QuestionsForMe))
	}

	if data.MediaSummary != nil {
		totalMedia := data.MediaSummary.PhotoCount + data.MediaSummary.VideoCount +
			data.MediaSummary.AudioCount + data.MediaSummary.DocumentCount
		if totalMedia > 0 {
			summary += fmt.Sprintf(", and %d media files", totalMedia)
		}
	}

	summary += "."

	if len(data.NeedsAttention) > 0 {
		summary += fmt.Sprintf(" %d chat(s) have unanswered questions.", len(data.NeedsAttention))
	}

	return summary
}

// ptrIfNotEmpty returns a pointer to the string if it's not empty, otherwise nil.
func ptrIfNotEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
