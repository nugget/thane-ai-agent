package email

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/mail"
)

// maxBodySize is the maximum body size to include in a message.
// Larger bodies are truncated with a note.
const maxBodySize = 32 * 1024

// ReadMessage fetches and parses a single message by UID from the
// specified folder. The MIME structure is walked to extract text/plain
// and text/html bodies.
func (c *Client) ReadMessage(ctx context.Context, folder string, uid uint32) (*Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return nil, err
	}

	if folder == "" {
		folder = "INBOX"
	}

	if _, err := c.selectFolder(folder); err != nil {
		return nil, err
	}

	uidSet := imap.UIDSet{}
	uidSet.AddNum(imap.UID(uid))

	fetchOpts := &imap.FetchOptions{
		UID:        true,
		Envelope:   true,
		Flags:      true,
		RFC822Size: true,
		BodySection: []*imap.FetchItemBodySection{
			{Peek: true}, // Fetch full body without marking as \Seen.
		},
	}

	fetchCmd := c.client.Fetch(uidSet, fetchOpts)

	msg := fetchCmd.Next()
	if msg == nil {
		_ = fetchCmd.Close()
		return nil, fmt.Errorf("message UID %d not found in %s", uid, folder)
	}

	result := &Message{}
	var bodyReader imap.LiteralReader

	for {
		item := msg.Next()
		if item == nil {
			break
		}

		switch data := item.(type) {
		case imapclient.FetchItemDataUID:
			result.UID = uint32(data.UID)
		case imapclient.FetchItemDataFlags:
			for _, f := range data.Flags {
				result.Flags = append(result.Flags, string(f))
			}
		case imapclient.FetchItemDataRFC822Size:
			result.Size = uint32(data.Size)
		case imapclient.FetchItemDataEnvelope:
			if data.Envelope != nil {
				result.Date = data.Envelope.Date
				result.Subject = data.Envelope.Subject
				if len(data.Envelope.From) > 0 {
					result.From = formatAddress(data.Envelope.From[0])
				}
				for _, addr := range data.Envelope.To {
					result.To = append(result.To, formatAddress(addr))
				}
			}
		case imapclient.FetchItemDataBodySection:
			bodyReader = data.Literal
		}
	}

	// Parse the message body.
	if bodyReader != nil {
		if err := c.parseBody(result, bodyReader); err != nil {
			c.logger.Debug("body parse error", "uid", uid, "error", err)
		}
	}

	if err := fetchCmd.Close(); err != nil {
		return nil, fmt.Errorf("fetch message UID %d: %w", uid, err)
	}

	return result, nil
}

// parseBody walks the MIME structure and extracts text content.
func (c *Client) parseBody(msg *Message, r io.Reader) error {
	mailReader, err := mail.CreateReader(r)
	if err != nil {
		return fmt.Errorf("create mail reader: %w", err)
	}

	for {
		part, err := mailReader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("next part: %w", err)
		}

		// Determine content type by checking the header type.
		var contentType string
		switch h := part.Header.(type) {
		case *mail.InlineHeader:
			contentType, _, _ = h.ContentType()
		case *mail.AttachmentHeader:
			// Skip attachment bodies.
			continue
		default:
			continue
		}

		switch {
		case contentType == "text/plain" && msg.TextBody == "":
			body, err := io.ReadAll(io.LimitReader(part.Body, maxBodySize+1))
			if err != nil {
				c.logger.Debug("error reading text/plain part", "error", err)
				continue
			}
			text := string(body)
			if len(body) > maxBodySize {
				text = text[:maxBodySize] + "\n\n[truncated — message exceeds 32KB]"
			}
			msg.TextBody = strings.TrimSpace(text)

		case contentType == "text/html" && msg.HTMLBody == "":
			body, err := io.ReadAll(io.LimitReader(part.Body, maxBodySize+1))
			if err != nil {
				c.logger.Debug("error reading text/html part", "error", err)
				continue
			}
			text := string(body)
			if len(body) > maxBodySize {
				text = text[:maxBodySize] + "\n\n[truncated — message exceeds 32KB]"
			}
			msg.HTMLBody = strings.TrimSpace(text)
		}
	}

	return nil
}
