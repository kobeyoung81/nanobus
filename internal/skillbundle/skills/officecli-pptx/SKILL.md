---
name: officecli-pptx
description: Create, inspect, edit, and validate .pptx presentations with OfficeCLI.
metadata: {"nanobot":{"requires":{"bins":["officecli"]}}}
---

# OfficeCLI PPTX Skill

Use this skill any time a `.pptx` slide deck is involved.

## Verify

```bash
officecli --version
```

## Read and Inspect

```bash
officecli view slides.pptx text
officecli view slides.pptx outline
officecli view slides.pptx annotated
officecli view slides.pptx html --browser
officecli get slides.pptx '/slide[1]' --depth 1
officecli query slides.pptx 'shape:contains("Revenue")'
officecli validate slides.pptx
```

## Routing

- Creating a new deck: read `creating.md`
- Editing an existing deck or template: read `editing.md`

## Rules

- Quote slide paths such as `'/slide[1]'` in the shell.
- Run one command at a time and verify after structural changes.
- Prefer `view html --browser` for full visual QA; SVG is good for one slide at a time.
- Content slides should include a real visual element, not only text blocks.
