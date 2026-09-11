package email

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
)

// maxBodySize is the most of a text part that is kept. Larger parts
// are cut on a rune boundary and [Message.BodyTruncated] is set.
const maxBodySize = 32 * 1024

// maxRawMessageSize is the most of a raw RFC 5322 message that is
// buffered from the IMAP literal. The remainder is drained so the
// stream stays in sync, [Message.RawTruncated] is set, and parts past
// the cut are never seen.
const maxRawMessageSize = 5 * 1024 * 1024

// errMissingUID marks a FETCH response with no UID item, which cannot
// be attributed to a message.
var errMissingUID = errors.New("fetch response missing UID")

// ReadMessage fetches one message and parses its MIME structure into a
// [Message]: envelope, threading headers, the readable body, and a
// description of attachments. The body is fetched with BODY.PEEK when
// opts.Peek is set, leaving the unseen state alone; otherwise the
// server marks the message seen. A UID the folder does not have is
// [FailureMessageNotFound].
func (c *Client) ReadMessage(ctx context.Context, opts ReadOptions) (*Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return nil, err
	}
	folder := normalizeFolder(opts.Folder)

	if _, err := c.selectFolder(ctx, folder, opts.Peek); err != nil {
		return nil, err
	}

	uidSet := imap.UIDSet{}
	uidSet.AddNum(imap.UID(opts.UID))

	fetchOpts := &imap.FetchOptions{
		UID:         true,
		Envelope:    true,
		Flags:       true,
		RFC822Size:  true,
		BodySection: []*imap.FetchItemBodySection{{Peek: opts.Peek}},
	}

	release := c.guard(ctx)
	defer release()

	fetchCmd := c.client.Fetch(uidSet, fetchOpts)
	msg := fetchCmd.Next()
	if msg == nil {
		if err := fetchCmd.Close(); err != nil {
			return nil, c.wrap(ctx, "fetch message", folder, opts.UID, err)
		}
		return nil, &ClientError{Account: c.name, Op: "fetch message", Folder: folder, UID: opts.UID, Kind: FailureMessageNotFound}
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
			applyEnvelope(&result.Envelope, data.Envelope)
		case imapclient.FetchItemDataBodySection:
			// Consume the literal immediately. go-imap/v2 streams data
			// from the connection and msg.Next() advances past unread
			// literals, so deferring the read would lose the body.
			if data.Literal == nil {
				c.logger.Debug("nil body literal", "uid", opts.UID)
				continue
			}
			var readErr error
			rawBody, readErr = io.ReadAll(io.LimitReader(data.Literal, maxRawMessageSize))
			// Drain whatever is left so the IMAP stream stays in sync,
			// and count it so RawTruncated is honest.
			if rest, _ := io.Copy(io.Discard, data.Literal); rest > 0 {
				result.RawTruncated = true
			}
			if readErr != nil {
				c.logger.Debug("error reading body literal", "uid", opts.UID, "error", readErr)
				rawBody = nil
			}
		}
	}
	if err := fetchCmd.Close(); err != nil {
		return nil, c.wrap(ctx, "fetch message", folder, opts.UID, err)
	}

	if rawBody != nil {
		if err := c.parseBody(result, bytes.NewReader(rawBody)); err != nil {
			c.logger.Debug("body parse error", "uid", opts.UID, "error", err)
		}
	}
	finishBody(result)
	return result, nil
}

// parseBody walks the MIME structure and extracts the text bodies,
// the References header (not available from the IMAP envelope), and a
// description of every non-text part.
//
// go-message's mail.CreateReader and NextPart may return both a valid
// reader and an error when a part uses an unknown charset or transfer
// encoding. Those are non-fatal: the content may be slightly garbled
// but is still useful for triage, so parsing continues.
func (c *Client) parseBody(msg *Message, r io.Reader) error {
	mailReader, err := mail.CreateReader(r)
	if err != nil && !message.IsUnknownCharset(err) {
		return err
	}
	if mailReader == nil {
		if err != nil {
			return err
		}
		return errors.New("mail reader is nil")
	}
	if err != nil {
		c.logger.Debug("mail reader created with charset warning", "error", err)
	}

	if refs, err := mailReader.Header.MsgIDList("References"); err == nil && len(refs) > 0 {
		msg.References = refs
	}

	for {
		part, err := mailReader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && !message.IsUnknownCharset(err) {
			return err
		}
		if part == nil {
			continue
		}
		if err != nil {
			c.logger.Debug("part has charset warning", "error", err)
		}

		switch h := part.Header.(type) {
		case *mail.InlineHeader:
			contentType, _, _ := h.ContentType()
			contentType = strings.ToLower(contentType)
			switch {
			case contentType == "text/plain" && msg.TextBody == "":
				body, truncated := readBounded(part.Body, maxBodySize)
				msg.TextBody = strings.TrimSpace(body)
				msg.BodyTruncated = msg.BodyTruncated || truncated
			case contentType == "text/html" && msg.HTMLBody == "":
				body, truncated := readBounded(part.Body, maxBodySize)
				msg.HTMLBody = strings.TrimSpace(body)
				msg.BodyTruncated = msg.BodyTruncated || truncated
			case strings.HasPrefix(contentType, "text/"):
				// A further text part (a second alternative, a quoted
				// original) is not the body; count it as inline content.
				msg.Attachments = append(msg.Attachments, describePart(h.Header, contentType, part.Body, true))
			default:
				msg.Attachments = append(msg.Attachments, describePart(h.Header, contentType, part.Body, true))
			}
		case *mail.AttachmentHeader:
			contentType, _, _ := h.ContentType()
			att := describePart(h.Header, strings.ToLower(contentType), part.Body, false)
			if name, err := h.Filename(); err == nil {
				att.Filename = name
			}
			msg.Attachments = append(msg.Attachments, att)
		default:
			_, _ = io.Copy(io.Discard, part.Body)
		}
	}
	return nil
}

// describePart records a non-body part without keeping its content.
// The body is drained through a counter so the size reported is the
// decoded size the recipient would download.
func describePart(h message.Header, contentType string, body io.Reader, inline bool) Attachment {
	att := Attachment{ContentType: contentType, Inline: inline}
	if _, params, err := h.ContentType(); err == nil {
		att.Filename = params["name"]
	}
	if n, err := io.Copy(io.Discard, body); err == nil {
		att.Size = n
	}
	return att
}

// readBounded reads at most limit bytes of r plus a probe byte to
// learn whether more followed, cuts on a rune boundary, and drains the
// rest so the enclosing MIME reader stays positioned. It returns the
// text and whether it was truncated.
func readBounded(r io.Reader, limit int) (string, bool) {
	buf, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return string(buf), false
	}
	truncated := len(buf) > limit
	if truncated {
		// Keep the probe byte: truncateUTF8 inspects s[limit] to
		// decide whether the cut lands inside a multi-byte rune.
		_, _ = io.Copy(io.Discard, r)
	}
	return truncateUTF8(string(buf), limit), truncated
}

// truncateUTF8 cuts s to at most maxBytes on a rune boundary, so a
// multi-byte character straddling the cut is dropped whole rather than
// left as an invalid prefix.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

// finishBody settles the readable body: a text part wins, an HTML-only
// message is rendered to text, and BodySource records which happened.
func finishBody(msg *Message) {
	switch {
	case msg.TextBody != "":
		msg.BodySource = "text"
	case msg.HTMLBody != "":
		msg.TextBody = htmlToText(msg.HTMLBody)
		msg.BodySource = "html"
	}
}
