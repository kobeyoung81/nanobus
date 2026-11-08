---
name: officecli
description: Create, inspect, validate, and modify Office documents (.docx, .xlsx, .pptx) with the officecli binary.
metadata: {"nanobot":{"requires":{"bins":["officecli"]}}}
---

# OfficeCLI Skill

Use this skill whenever a `.docx`, `.xlsx`, or `.pptx` file is part of the task.

## Verify

```bash
officecli --version
```

## Core Strategy

Prefer this escalation path:

1. **L1 Read** — `view`, `get`, `query`, `validate`
2. **L2 DOM edits** — `set`, `add`, `remove`, `move`, `swap`
3. **L3 Raw XML** — `raw`, `raw-set` only when the high-level CLI cannot express the change

## Working Rules

- Run commands one at a time.
- For 3+ operations on the same file, use `officecli open <file>` and `officecli close <file>`.
- Re-run `get` or `validate` after structural changes before building on top of them.
- When unsure about property names, ask the CLI for help instead of guessing:

```bash
officecli docx set
officecli xlsx set
officecli pptx set
```

## Specialized Guides

- Word documents: `../officecli-docx/SKILL.md`
- Excel workbooks: `../officecli-xlsx/SKILL.md`
- PowerPoint decks: `../officecli-pptx/SKILL.md`
