package app

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// documentRootProvenanceReviser adapts a provenance.Reader (a signing Store or
// a verify-only Verifier) to documents.RootReviser, translating the root's
// prefix and the provenance revision/signer types into the documents-layer
// forms so the documents package never depends on provenance.
type documentRootProvenanceReviser struct {
	reader provenance.Reader
	prefix string
}

var _ documents.RootReviser = (*documentRootProvenanceReviser)(nil)

func (r *documentRootProvenanceReviser) storeFilename(filename string) string {
	return repoPrefixedFilename(r.prefix, filename)
}

func (r *documentRootProvenanceReviser) Resolve(ctx context.Context, filename, selector string) (documents.RevisionRef, error) {
	rev, err := r.reader.ResolveRevision(ctx, r.storeFilename(filename), selector)
	if err != nil {
		return documents.RevisionRef{}, err
	}
	return revisionRefFromProvenance(rev), nil
}

func (r *documentRootProvenanceReviser) History(ctx context.Context, filename string, opts documents.RevisionQuery) (documents.RevisionListing, error) {
	page, err := r.reader.Revisions(ctx, r.storeFilename(filename), provenance.RevisionOptions{
		Limit:       opts.Limit,
		Before:      opts.Before,
		WithSigners: opts.WithSigners,
	})
	if err != nil {
		return documents.RevisionListing{}, err
	}
	out := documents.RevisionListing{Total: page.Total, NextBefore: page.NextBefore}
	for _, rev := range page.Revisions {
		out.Revisions = append(out.Revisions, revisionRefFromProvenance(rev))
	}
	return out, nil
}

func (r *documentRootProvenanceReviser) Diff(ctx context.Context, filename, from, to, format string) (documents.RevisionDiff, error) {
	sf := r.storeFilename(filename)
	base, err := r.reader.ResolveRevision(ctx, sf, from)
	if err != nil {
		return documents.RevisionDiff{}, fmt.Errorf("resolve from: %w", err)
	}
	target, err := r.reader.ResolveRevision(ctx, sf, to)
	if err != nil {
		return documents.RevisionDiff{}, fmt.Errorf("resolve to: %w", err)
	}
	// Order by ancestry so the older revision is always the diff base,
	// regardless of the order the caller passed the endpoints. Index (commits
	// newer than this one) is reliable even when two commits share a second,
	// where author timestamps would tie.
	if base.Index < target.Index {
		base, target = target, base
	}
	diff, err := r.reader.Diff(ctx, base.Commit, target.Commit, sf, provenance.DiffFormat(format))
	if err != nil {
		return documents.RevisionDiff{}, err
	}
	return documents.RevisionDiff{
		Base:    revisionRefFromProvenance(base),
		Target:  revisionRefFromProvenance(target),
		Format:  string(diff.Format),
		Added:   diff.Added,
		Removed: diff.Removed,
		Body:    diff.Body,
	}, nil
}

func (r *documentRootProvenanceReviser) Content(ctx context.Context, filename, selector string) (documents.RevisionContent, error) {
	sf := r.storeFilename(filename)
	rev, err := r.reader.ResolveRevision(ctx, sf, selector)
	if err != nil {
		return documents.RevisionContent{}, err
	}
	ref := revisionRefFromProvenance(rev)
	// Attach the signer so the caller can gate content on trust. A signer
	// lookup failure is non-fatal — the content is still returned, unsigned.
	if signer, sErr := r.reader.SignerFor(ctx, rev.Commit); sErr == nil {
		s := revisionSignerFromProvenance(signer)
		ref.Signer = &s
	}
	content, err := r.reader.Blob(ctx, rev.Commit, sf)
	if err != nil {
		return documents.RevisionContent{}, err
	}
	return documents.RevisionContent{Revision: ref, Content: content}, nil
}

func revisionRefFromProvenance(rev provenance.Revision) documents.RevisionRef {
	ref := documents.RevisionRef{
		Commit:    rev.Commit,
		Short:     rev.Short,
		Index:     rev.Index,
		Timestamp: rev.Timestamp,
		Message:   rev.Message,
	}
	if rev.Signer != nil {
		s := revisionSignerFromProvenance(*rev.Signer)
		ref.Signer = &s
	}
	return ref
}

func revisionSignerFromProvenance(cs provenance.CommitSigner) documents.RevisionSigner {
	return documents.RevisionSigner{
		Verified:       cs.Verified,
		Principal:      cs.Principal,
		Kind:           cs.Kind,
		KeyFingerprint: cs.KeyFingerprint,
		Reason:         cs.Reason,
	}
}

// repoPrefixedFilename joins a root-relative filename onto the repo prefix
// (when the git repo is a parent of the root), leaving traversal/absolute
// inputs untouched so downstream validation can reject them.
func repoPrefixedFilename(prefix, filename string) string {
	clean := filepath.ToSlash(filepath.Clean(filename))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return clean
	}
	if prefix == "" || prefix == "." {
		return clean
	}
	return path.Join(prefix, clean)
}
