# Development Working Directory

This directory mirrors the production `~/Thane/` layout. `just serve`
runs Thane from here, so all relative paths in your config resolve
against this directory — same as production.

## Layout

```
Thane/
├── config.yaml       # Site-specific config (cp ../config.example.yaml)
├── persona.md        # Agent personality (replaces default system prompt)
├── talents/          # Skill/behavior markdown files (extend system prompt)
│   └── *.md
├── data/             # SQLite databases — facts, memory, checkpoints (auto-created)
├── workspace/        # Agent's sandboxed file access (if configured)
└── thane.log         # Log output
```

## Config defaults

All paths default to locations inside this directory:

| Config key      | Default         | What it is                          |
|-----------------|-----------------|-------------------------------------|
| `persona_file`  | `./persona.md`  | Agent personality                   |
| `talents_dir`   | `./talents`     | Skill markdown files                |
| `data_dir`      | `./data`        | SQLite databases                    |
| `workspace.root`| *(none)*        | Sandboxed file access for the agent |

## Getting started

```bash
cp config.example.yaml Thane/config.yaml
# Edit Thane/config.yaml with your settings
just serve
```

Everything in this directory except this README is gitignored —
it contains secrets and local state.
