# Contributing to Thane

Thank you for your interest in contributing to Thane!

## Ways to Contribute

### Ideas & Discussion

- Open an issue to discuss features or architecture
- Share your Home Assistant use cases that could benefit from an autonomous agent
- Provide feedback on the design in [ARCHITECTURE.md](ARCHITECTURE.md)

### Code

- Follow the existing code style
- Write tests for new functionality
- Use conventional commits (see below)
- Keep PRs focused — one feature or fix per PR

### Documentation

- Improve README, ARCHITECTURE, or code comments
- Add examples and use cases
- Fix typos and clarify confusing sections

## Development Setup

### Prerequisites

- [Go](https://go.dev/) 1.24+
- [just](https://just.systems/) (command runner)
- [golangci-lint](https://golangci-lint.run/) v2.x

### Workflow

```bash
# Clone and build
git clone https://github.com/nugget/thane-ai-agent.git
cd thane-ai-agent
just build

# Run the full CI gate before pushing
just ci    # fmt check + lint + tests with -race

# Individual steps
just test        # Tests only
just lint        # Linter only
just fmt-check   # Format check only
```

All workflows go through `just`. Don't call `gofmt`, `go vet`, or `go test` directly — the justfile is the interface.

## Commit Messages

We use [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add home assistant websocket client
fix: handle reconnection on connection loss
docs: clarify model routing configuration
refactor: extract tool executor into separate package
test: add integration tests for HA client
chore: update dependencies
```

## Testing

```bash
just test    # Always runs with -race detector
just ci      # Full gate: format + lint + test
```

## Questions?

Open an issue or start a discussion. We're happy to help!

## License

By contributing, you agree that your contributions will be licensed under the Apache 2.0 license.
