// Package documents indexes managed local markdown roots for model-facing
// rediscovery, browse, search, and section retrieval.
//
// The operator-facing contract for what counts as a managed document root
// lives in docs/understanding/document-roots.md. Keep that document in
// sync with behavioral changes here, especially when changing how config
// paths become indexed corpora or how the documents capability is meant
// to differ from raw file access.
package documents
