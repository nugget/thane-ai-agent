# Development Working Directory

This directory mirrors the production `~/Thane/` layout. Put your
site-specific files here for local development with `just serve`.

## Expected contents

```
Thane/
├── config.yaml     # Your config (cp ../config.example.yaml config.yaml)
├── persona.md      # Agent personality
├── data/           # SQLite databases (created automatically)
└── thane.log       # Log output (when running via just serve)
```

Everything in this directory except this README is gitignored —
it contains secrets and local state.

## Getting started

```bash
cp config.example.yaml Thane/config.yaml
# Edit Thane/config.yaml with your settings
just serve
```
