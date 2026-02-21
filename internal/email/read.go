package email

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
)

// maxBodySize is the maximum body size to include in a message.
// Larger bodies are truncated with a note.
const maxBodySize = 32 * 1024

// maxRawMessageSize is the maximum raw RFC822 message size to buffer
// when reading from the IMAP literal. Messages larger than this (e.g.
// with huge attachments) are truncated — the remainder of the literal
// is drained to keep the IMAP stream in sync. The parsed text body
// is further truncated at maxBodySize by parseBody.
const maxRawMessageSize = 5 * 1024 * 1024

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
			{Peek: false}, // Mark as \Seen — reading means read.
		},
	}

	fetchCmd := c.client.Fetch(uidSet, fetchOpts)

	msg := fetchCmd.Next()
	if msg == nil {
		_ = fetchCmd.Close()
		return nil, fmt.Errorf("message UID %d not found in %s", uid, folder)
	}

	result := &Message{}
	var rawBody []byte

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
				result.MessageID = data.Envelope.MessageID
				result.InReplyTo = data.Envelope.InReplyTo
				if len(data.Envelope.From) > 0 {
					result.From = formatAddress(data.Envelope.From[0])
				}
				for _, addr := range data.Envelope.To {
					result.To = append(result.To, formatAddress(addr))
				}
				for _, addr := range data.Envelope.Cc {
					result.Cc = append(result.Cc, formatAddress(addr))
				}
				if len(data.Envelope.ReplyTo) > 0 {
					result.ReplyTo = formatAddress(data.Envelope.ReplyTo[0])
				}
			}
		case imapclient.FetchItemDataBodySection:
			// Consume the literal immediately. go-imap/v2 streams
			// data from the IMAP connection; msg.Next() advances
			// past unread literals, so deferring the read would
			// lose the body data.
			if data.Literal == nil {
				c.logger.Debug("nil body literal", "uid", uid)
				continue
			}
			var readErr error
			rawBody, readErr = io.ReadAll(io.LimitReader(data.Literal, maxRawMessageSize))
			// Drain any remaining data so the IMAP stream stays in sync.
			_, _ = io.Copy(io.Discard, data.Literal)
			if readErr != nil {
				c.logger.Debug("error reading body literal", "uid", uid, "error", readErr)
				rawBody = nil
			}
		}
	}

	// Parse the message body from the buffered bytes.
	if rawBody != nil {
		if err := c.parseBody(result, bytes.NewReader(rawBody)); err != nil {
			c.logger.Debug("body parse error", "uid", uid, "error", err)
		}
	}

	if err := fetchCmd.Close(); err != nil {
		return nil, fmt.Errorf("fetch message UID %d: %w", uid, err)
	}

	return result, nil
}

// parseBody walks the MIME structure and extracts text content and
// the References header (not available from the IMAP Envelope).
//
// The go-message library's mail.CreateReader and NextPart may return
// both a valid reader/part AND an error when the message uses an
// unknown charset or transfer encoding. We treat those as non-fatal
// and continue parsing — the content may be slightly garbled but is
// still useful for triage.
func (c *Client) parseBody(msg *Message, r io.Reader) error {
	mailReader, err := mail.CreateReader(r)
	if err != nil && !message.IsUnknownCharset(err) {
		return fmt.Errorf("create mail reader: %w", err)
	}
	if mailReader == nil {
		if err != nil {
			return fmt.Errorf("create mail reader returned nil: %w", err)
		}
		return fmt.Errorf("create mail reader returned nil")
	}
	if err != nil {
		c.logger.Debug("mail reader created with charset warning", "error", err)
	}

	// Extract References from the top-level mail header.
	// This is not available in the IMAP ENVELOPE; it must be parsed
	// from the raw message.
	if refs, err := mailReader.Header.MsgIDList("References"); err == nil && len(refs) > 0 {
		msg.References = refs
	}

	for {
		part, err := mailReader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && !message.IsUnknownCharset(err) {
			return fmt.Errorf("next part: %w", err)
		}
		if part == nil {
			continue
		}
		if err != nil {
			c.logger.Debug("part has charset warning", "error", err)
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
