package documents

import "time"

// DocumentLink is one outgoing link projected from an indexed document.
type DocumentLink struct {
	Target string `json:"target"`
	Kind   string `json:"kind"`
	Ref    string `json:"ref,omitempty"`
	Title  string `json:"title,omitempty"`
	URL    string `json:"url,omitempty"`
	Anchor string `json:"anchor,omitempty"`
}

// Backlink is one indexed document that links to another document.
type Backlink struct {
	Ref        string   `json:"ref"`
	Path       string   `json:"path"`
	Title      string   `json:"title"`
	ModifiedAt string   `json:"modified_at"`
	Targets    []string `json:"targets,omitempty"`
}

// LinksResult is the outgoing/backlink view for one indexed document.
type LinksResult struct {
	Ref       string         `json:"ref"`
	Mode      string         `json:"mode"`
	Outgoing  []DocumentLink `json:"outgoing,omitempty"`
	Backlinks []Backlink     `json:"backlinks,omitempty"`
}

// SearchQuery filters document search results.
type SearchQuery struct {
	Root            string              `json:"root,omitempty"`
	PathPrefix      string              `json:"path_prefix,omitempty"`
	Query           string              `json:"query,omitempty"`
	Tags            []string            `json:"tags,omitempty"`
	Frontmatter     map[string][]string `json:"frontmatter,omitempty"`
	FrontmatterKeys []string            `json:"frontmatter_keys,omitempty"`
	ModifiedAfter   *time.Time          `json:"-"`
	ModifiedBefore  *time.Time          `json:"-"`
	Limit           int                 `json:"limit,omitempty"`
}
