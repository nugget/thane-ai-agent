// Package email is Thane's native IMAP and SMTP channel: it reads,
// searches, files, and sends mail for one or more configured accounts
// and turns new inbound messages into loop wakes.
//
// # Accounts, folders, and UIDs
//
// Every operation names an account ([AccountConfig.Name]) and a folder.
// A message is identified by its IMAP UID, and a UID is meaningful only
// within one folder of one account: after a move the message has a new
// UID in the destination, which [MoveResult.DestUIDs] reports when the
// server supports UIDPLUS. Results therefore always carry the account
// and folder a UID was resolved in, and callers should pass both back.
// An omitted folder means [DefaultFolder].
//
// # Addresses
//
// Mailboxes are [Address] values: a display name and an addr-spec.
// Comparison goes through [Address.Key], which lowercases the address
// so the same mailbox spelled two ways is one mailbox. Group markers
// in IMAP envelopes (entries with no address) are dropped.
//
// # Connections and cancellation
//
// [Client] holds one IMAP connection per account and serializes every
// operation behind a mutex, because a selected mailbox is
// connection-wide state and two callers selecting different folders
// on one connection would interleave. The caller's context bounds each
// operation: a watchdog closes the connection when the context ends or
// when defaultOpTimeout passes without the command completing, so a
// blocked command returns instead of holding the account. The next
// call reconnects. Every connection is TLS before a credential is sent,
// by handshake on an implicit-TLS port or by STARTTLS on a plaintext
// one, and a server that offers neither is refused. SMTP connections
// are per-send and follow the same rules.
//
// # Failures
//
// Failures the model can act on are [*ClientError] values whose
// [FailureKind] says what to do differently: a missing folder lists
// the folders that exist, a missing message names the folder and
// account it was looked up in, an authentication failure says the
// operator must act. SMTP delivery failures are [*DeliveryError]
// values distinguishing permanent rejection from transient refusal.
//
// # Polling and wakes
//
// [Poller] compares each account's INBOX against a persisted
// high-water mark ([Poller.CheckNewMessages]) and dispatches every new
// message as an event to the configured handler loop, stamping the
// account, folder, UID, Message-ID, and the sender's trust zone on the
// event so the receiving iteration never has to guess which mailbox a
// number belongs to. The mark carries UIDVALIDITY, so a rebuilt mailbox
// reseeds instead of flooding.
package email
