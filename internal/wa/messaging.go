package wa

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.mau.fi/whatsmeow"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"github.com/eddmann/whatsapp-mcp/internal/media"
)

// SendText sends a text message to a JID or phone number string (without +) or group JID.
func (c *Client) SendText(recipient, text string) (bool, string, error) {
	if !c.WA.IsConnected() {
		return false, "not connected", fmt.Errorf("not connected")
	}

	jid, err := parseRecipient(recipient)
	if err != nil {
		return false, "invalid recipient", err
	}

	msg := &waE2E.Message{Conversation: protoString(text)}
	_, err = c.WA.SendMessage(context.Background(), jid, msg)
	if err != nil {
		return false, err.Error(), err
	}

	return true, fmt.Sprintf("sent to %s", recipient), nil
}

// SendMedia sends an image/video/document/audio with optional caption; audio is PTT if .ogg.
func (c *Client) SendMedia(recipient, path, caption string) (bool, string, error) {
	if !c.WA.IsConnected() {
		return false, "not connected", fmt.Errorf("not connected")
	}

	jid, err := parseRecipient(recipient)
	if err != nil {
		return false, "invalid recipient", err
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return false, "read error", err
	}

	mediaType, mime := classify(path)
	up, err := c.WA.Upload(context.Background(), b, mediaType)
	if err != nil {
		return false, "upload failed", err
	}

	m := &waE2E.Message{}
	base := filepath.Base(path)

	switch mediaType {
	case whatsmeow.MediaImage:
		m.ImageMessage = &waE2E.ImageMessage{
			Caption:        protoString(caption),
			Mimetype:       protoString(mime),
			URL:            &up.URL,
			DirectPath:     &up.DirectPath,
			MediaKey:       up.MediaKey,
			FileEncSHA256:  up.FileEncSHA256,
			FileSHA256:     up.FileSHA256,
			FileLength:     &up.FileLength,
		}
	case whatsmeow.MediaVideo:
		m.VideoMessage = &waE2E.VideoMessage{
			Caption:        protoString(caption),
			Mimetype:       protoString(mime),
			URL:            &up.URL,
			DirectPath:     &up.DirectPath,
			MediaKey:       up.MediaKey,
			FileEncSHA256:  up.FileEncSHA256,
			FileSHA256:     up.FileSHA256,
			FileLength:     &up.FileLength,
		}
	case whatsmeow.MediaDocument:
		m.DocumentMessage = &waE2E.DocumentMessage{
			Title:          protoString(base),
			Caption:        protoString(caption),
			Mimetype:       protoString(mime),
			URL:            &up.URL,
			DirectPath:     &up.DirectPath,
			MediaKey:       up.MediaKey,
			FileEncSHA256:  up.FileEncSHA256,
			FileSHA256:     up.FileSHA256,
			FileLength:     &up.FileLength,
		}
	case whatsmeow.MediaAudio:
		// If not .ogg, convert via ffmpeg
		if !isOgg(path) {
			cpath, err := media.ConvertToOpusOgg(path)
			if err != nil {
				return false, "conversion failed", err
			}
			defer func() { _ = os.Remove(cpath) }()

			b2, err := os.ReadFile(cpath)
			if err != nil {
				return false, "read converted", err
			}

			up2, err := c.WA.Upload(context.Background(), b2, whatsmeow.MediaAudio)
			if err != nil {
				return false, "upload converted", err
			}

			dur, waveform, _ := media.AnalyzeOggOpus(b2)
			m.AudioMessage = &waE2E.AudioMessage{
				Mimetype:       protoString("audio/ogg; codecs=opus"),
				URL:            &up2.URL,
				DirectPath:     &up2.DirectPath,
				MediaKey:       up2.MediaKey,
				FileEncSHA256:  up2.FileEncSHA256,
				FileSHA256:     up2.FileSHA256,
				FileLength:     &up2.FileLength,
				Seconds:        protoUint32(uint32(dur)),
				PTT:            protoBool(true),
				Waveform:       waveform,
			}
		} else {
			dur, waveform, _ := media.AnalyzeOggOpus(b)
			m.AudioMessage = &waE2E.AudioMessage{
				Mimetype:       protoString(mime),
				URL:            &up.URL,
				DirectPath:     &up.DirectPath,
				MediaKey:       up.MediaKey,
				FileEncSHA256:  up.FileEncSHA256,
				FileSHA256:     up.FileSHA256,
				FileLength:     &up.FileLength,
				Seconds:        protoUint32(uint32(dur)),
				PTT:            protoBool(true),
				Waveform:       waveform,
			}
		}
	}

	_, err = c.WA.SendMessage(context.Background(), jid, m)
	if err != nil {
		return false, err.Error(), err
	}

	return true, fmt.Sprintf("sent media to %s", recipient), nil
}

// DownloadMedia looks up media from DB and downloads via whatsmeow.
func (c *Client) DownloadMedia(messageID, chatJID string) (bool, string, string, string, error) {
	var mediaType, filename, url string
	var mediaKey, fileSHA256, fileEncSHA256 []byte
	var fileLength uint64

	row := c.Store.Messages.QueryRow("SELECT media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length FROM messages WHERE id = ? AND chat_jid = ?", messageID, chatJID)
	if err := row.Scan(&mediaType, &filename, &url, &mediaKey, &fileSHA256, &fileEncSHA256, &fileLength); err != nil {
		return false, "", "", "", err
	}

	if mediaType == "" || url == "" || len(mediaKey) == 0 || len(fileSHA256) == 0 || len(fileEncSHA256) == 0 || fileLength == 0 {
		return false, "", "", "", fmt.Errorf("incomplete media info")
	}

	dp := extractDirectPathFromURL(url)
	dm := &downloadable{
		URL:           url,
		DirectPath:    dp,
		MediaKey:      mediaKey,
		FileLength:    fileLength,
		FileSHA256:    fileSHA256,
		FileEncSHA256: fileEncSHA256,
		MediaType:     classifyToWA(mediaType),
	}

	data, err := c.WA.Download(context.Background(), dm)
	if err != nil {
		return false, "", "", "", err
	}

	outDir := filepath.Join(c.BaseDir, strings.ReplaceAll(chatJID, ":", "_"))
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return false, "", "", "", err
	}

	out := filepath.Join(outDir, filename)
	if err := os.WriteFile(out, data, fs.FileMode(0644)); err != nil {
		return false, "", "", "", err
	}

	abs, _ := filepath.Abs(out)
	return true, mediaType, filename, abs, nil
}

// protoString returns a pointer to a string (for protobuf).
func protoString(s string) *string { return &s }

// protoBool returns a pointer to a bool (for protobuf).
func protoBool(b bool) *bool { return &b }

// protoUint32 returns a pointer to a uint32 (for protobuf).
func protoUint32(u uint32) *uint32 { return &u }

// parseRecipient parses a recipient string (phone or JID) into a types.JID.
func parseRecipient(recipient string) (types.JID, error) {
	if strings.Contains(recipient, "@") {
		return types.ParseJID(recipient)
	}
	return types.JID{User: recipient, Server: "s.whatsapp.net"}, nil
}

// classify determines WhatsApp media type and MIME type from file extension.
func classify(path string) (whatsmeow.MediaType, string) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".jpg", ".jpeg":
		return whatsmeow.MediaImage, "image/jpeg"
	case ".png":
		return whatsmeow.MediaImage, "image/png"
	case ".gif":
		return whatsmeow.MediaImage, "image/gif"
	case ".webp":
		return whatsmeow.MediaImage, "image/webp"
	case ".mp4":
		return whatsmeow.MediaVideo, "video/mp4"
	case ".avi":
		return whatsmeow.MediaVideo, "video/avi"
	case ".mov":
		return whatsmeow.MediaVideo, "video/quicktime"
	case ".ogg":
		return whatsmeow.MediaAudio, "audio/ogg; codecs=opus"
	default:
		return whatsmeow.MediaDocument, "application/octet-stream"
	}
}
