---
name: file-organizer
description: "Use when the user asks to organize, restructure, or tidy a project's files and directories, when setting up a new project layout, or when reviewing whether an existing directory structure follows conventions. Covers proposals and conventions — not bulk deletion."
version: 2.0.0
author: go-magic
tags: [organization, files, structure, management]
tools: [list_files, directory_tree, file_edit, search_in_files]
---

# File Organizer Skill

Conventions and layouts for organizing a project's files.

## When to Use

Load this skill when:
- The user wants to organize or restructure project files
- Setting up the directory layout for a new project
- Tidying a messy directory into a consistent structure
- Reviewing whether an existing layout follows language and tool conventions

## Safety Rule (read before touching anything)

**Organizing means proposing and moving — not deleting.** Never delete, move, or rename files in a user's directory without (a) listing exactly what will be affected and (b) getting explicit confirmation for that list. A "cleanup" request is a request for a report first; act only on items the user approves, and prefer moving to a trash/archive location over permanent deletion.

## Project Layouts

### General
```
project/
├── README.md
├── LICENSE
├── .gitignore
├── src/              # source code
├── tests/            # test files
├── docs/             # documentation
├── config/           # configuration
├── scripts/          # build / utility scripts
└── bin/              # compiled binaries (usually gitignored)
```

### Go
```
project/
├── go.mod            # module path + deps (no vendor/ unless required)
├── Makefile
├── cmd/<binary>/      # one main package per binary
├── internal/          # not importable outside the module
├── pkg/               # importable by other projects
└── testdata/          # fixtures, ignored by the go tool
```

### Web / TypeScript
```
web-project/
├── package.json
├── public/           # static assets served as-is
└── src/
    ├── components/
    ├── pages/
    ├── hooks/
    └── utils/
```

### Python
```
python-project/
├── pyproject.toml     # preferred over setup.py
├── src/<package>/
│   ├── __init__.py
│   └── module.py
└── tests/
```

## Naming Conventions

Conventions are per-language, not universal — "files are kebab-case" is wrong for Go and Python. Follow the language's own rule:

| Language | Files | Types / Classes | Functions / Variables |
|---|---|---|---|
| Go | `snake_case.go`, test as `_test.go` | `PascalCase` | `camelCase` (exported: `PascalCase`) |
| Python | `snake_case.py` | `PascalCase` | `snake_case` |
| Java / Kotlin | `PascalCase.java` | `PascalCase` | `camelCase` |
| JS / TS | `kebab-case.ts` or `PascalCase.tsx` for components | `PascalCase` | `camelCase` |
| Rust | `snake_case.rs` | `PascalCase` | `snake_case` |
| Shell | `kebab-case.sh` | — | `snake_case` |
| Constants (most) | — | — | `UPPER_SNAKE_CASE` |

Universal rules that do hold: be internally consistent within a directory, and do not mix two conventions for the same kind of file.

## Organization Principles

1. **Group by function, not by file type** — keep a feature's code, tests, and fixtures together where the language allows
2. **Follow the ecosystem** — a Go project should look like Go, a Python project like Python; do not impose one layout on every language
3. **Limit depth** — if a path needs more than ~4 levels, the structure is describing the wrong thing
4. **Name by purpose** — `billing/` beats `misc/`; a name that could mean anything means nothing
5. **Keep generated artifacts out of source** — binaries, caches, and build output belong in gitignored directories

## Review Checklist (proposal, not an action list)

- [ ] Layout matches the language/tooling convention
- [ ] No source file sits in a generated-output directory
- [ ] `.gitignore` covers build output, caches, and secrets
- [ ] README explains the layout for anyone new
- [ ] Naming is consistent within each directory
- [ ] Obvious duplicates *reported* to the user (not deleted)

## Tools Integration

- `list_files` / `directory_tree` — inspect the current structure before proposing changes
- `search_in_files` — find stray references (imports, config paths) that a move would break
- `file_edit` — apply path references after an approved move
