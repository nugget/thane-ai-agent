// Package signal provides a native Go integration with signal-cli's
// JSON-RPC daemon mode for sending and receiving Signal messages.
package signal

// Envelope is the top-level structure pushed by signal-cli for each
// received event. Exactly one of the message-type fields will be
// non-nil.
type Envelope struct {
	Source       string `json:"source"`
	SourceNumber string `json:"sourceNumber"`
	SourceName   string `json:"sourceName"`
	SourceDevice int    `json:"sourceDevice"`
	Timestamp    int64  `json:"timestamp"`

	DataMessage    *DataMessage    `json:"dataMessage,omitempty"`
	TypingMessage  *TypingMessage  `json:"typingMessage,omitempty"`
	ReceiptMessage *ReceiptMessage `json:"receiptMessage,omitempty"`
	SyncMessage    *SyncMessage    `json:"syncMessage,omitempty"`
}

// DataMessage is a normal text or media message.
type DataMessage struct {
	Timestamp        int64      `json:"timestamp"`
	Message          string     `json:"message"`
	ExpiresInSeconds int        `json:"expiresInSeconds"`
	ViewOnce         bool       `json:"viewOnce"`
	GroupInfo        *GroupInfo `json:"groupInfo,omitempty"`
}

// GroupInfo identifies the group a message was sent to.
type GroupInfo struct {
	GroupID  string `json:"groupId"`
	Revision int    `json:"revision"`
	Type     string `json:"type"` // e.g., "DELIVER"
}

// TypingMessage indicates that a contact started or stopped typing.
type TypingMessage struct {
	Action    string `json:"action"` // "STARTED" or "STOPPED"
	Timestamp int64  `json:"timestamp"`
	GroupID   string `json:"groupId,omitempty"`
}

// ReceiptMessage is a delivery, read, or viewed receipt from another user.
type ReceiptMessage struct {
	When       int64   `json:"when"`
	Type       string  `json:"type"` // "DELIVERY", "READ", "VIEWED"
	Timestamps []int64 `json:"timestamps"`
}

// SyncMessage carries sync events from linked devices. We only define
// the fields we need; signal-cli emits many more.
type SyncMessage struct {
	ReadMessages []SyncRead `json:"readMessages,omitempty"`
}

// SyncRead marks a message as read on a linked device.
type SyncRead struct {
	Sender    string `json:"sender"`
	Timestamp int64  `json:"timestamp"`
}

// receiveNotification is the JSON-RPC notification payload for method
// "receive" pushed by signal-cli.
type receiveNotification struct {
	Envelope Envelope `json:"envelope"`
}

// sendResult is the response payload from a successful "send" RPC call.
type sendResult struct {
	Timestamp int64 `json:"timestamp"`
}
