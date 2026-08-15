package tools

import "context"

func (r *Registry) registerRepositoryGitTools() {
	rootParameter := map[string]any{
		"type":        "string",
		"description": "Named repository root. Omit when the loop has a repo_root binding; the binding supplies it. An unbound caller must name one returned by forge_repo_subscriptions.",
	}

	r.Register(&Tool{
		Name:               "repo_git_log",
		SkipContentResolve: true,
		Description:        "Read commit history from a named repository root. Use from and to for the commits in from..to; omit from for ordinary history ending at to. This is read-only and cannot access repositories outside the selected root.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"root":  rootParameter,
				"from":  map[string]any{"type": "string", "description": "Optional older commit/ref, commonly the prior last_synced_sha."},
				"to":    map[string]any{"type": "string", "description": "Newer commit/ref; defaults to HEAD."},
				"path":  map[string]any{"type": "string", "description": "Optional root-relative file or directory path."},
				"limit": map[string]any{"type": "integer", "description": "Maximum commits to return (default 20, max 100)."},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			limit := numberArg(args, "limit")
			return r.fileTools.RepositoryGitLog(ctx, textArg(args, "root"), textArg(args, "from"), textArg(args, "to"), textArg(args, "path"), limit)
		},
	})

	r.Register(&Tool{
		Name:               "repo_git_diff",
		SkipContentResolve: true,
		Description:        "Read the patch or diffstat between two commits in a named repository root. Large patches degrade to a diffstat instead of being clipped mid-hunk.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"root":   rootParameter,
				"from":   map[string]any{"type": "string", "description": "Base commit/ref. Required; use a prior last_synced_sha when inspecting a subscription wake."},
				"to":     map[string]any{"type": "string", "description": "Target commit/ref; defaults to HEAD."},
				"path":   map[string]any{"type": "string", "description": "Optional root-relative file or directory path."},
				"format": map[string]any{"type": "string", "enum": []string{"patch", "stat"}, "description": "Output form; defaults to patch."},
			},
			"required": []string{"from"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.fileTools.RepositoryGitDiff(ctx, textArg(args, "root"), textArg(args, "from"), textArg(args, "to"), textArg(args, "path"), textArg(args, "format"))
		},
	})

	r.Register(&Tool{
		Name:               "repo_git_show",
		SkipContentResolve: true,
		Description:        "Show one commit and its patch from a named repository root. Optionally restrict the patch to one root-relative path. Large patches degrade to a commit summary and diffstat.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"root":     rootParameter,
				"revision": map[string]any{"type": "string", "description": "Commit/ref to show; defaults to HEAD."},
				"path":     map[string]any{"type": "string", "description": "Optional root-relative file or directory path."},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.fileTools.RepositoryGitShow(ctx, textArg(args, "root"), textArg(args, "revision"), textArg(args, "path"))
		},
	})

	r.Register(&Tool{
		Name:               "repo_git_blame",
		SkipContentResolve: true,
		Description:        "Attribute lines in one file to commits in a named repository root. Returns structured line, author, commit, age, summary, and content fields.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"root":       rootParameter,
				"path":       map[string]any{"type": "string", "description": "Root-relative file path."},
				"revision":   map[string]any{"type": "string", "description": "Commit/ref to blame; defaults to HEAD."},
				"start_line": map[string]any{"type": "integer", "description": "Optional first line (1-indexed)."},
				"end_line":   map[string]any{"type": "integer", "description": "Optional last line (inclusive)."},
			},
			"required": []string{"path"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.fileTools.RepositoryGitBlame(ctx, textArg(args, "root"), textArg(args, "revision"), textArg(args, "path"), numberArg(args, "start_line"), numberArg(args, "end_line"))
		},
	})
}
