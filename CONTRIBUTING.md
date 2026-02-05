# Contributing to Thane

Thank you for your interest in contributing to Thane!

## Development Status

Thane is in early development. The architecture is defined but implementation is just starting. This is a great time to contribute ideas and discuss design decisions.

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

## Code Style

- Run `go fmt` before committing
- Run `go vet` and fix warnings
- Keep functions small and focused
- Prefer clarity over cleverness

## Testing

```bash
go test ./...
```

## Questions?

Open an issue or start a discussion. We're happy to help!

## License

By contributing, you agree that your contributions will be licensed under the Apache 2.0 license.
