---
name: officecli-docx
description: Create, inspect, edit, and validate .docx files with OfficeCLI.
metadata: {"nanobot":{"requires":{"bins":["officecli"]}}}
---

# OfficeCLI DOCX Skill

Use this skill any time a `.docx` file is involved.

## Verify

```bash
officecli --version
```

## Read and Inspect

```bash
officecli view doc.docx text
officecli view doc.docx outline
officecli view doc.docx annotated
officecli view doc.docx issues
officecli get doc.docx /body --depth 1
officecli query doc.docx 'paragraph[style=Heading1]'
officecli validate doc.docx
```

## Routing

- Creating a new document: read `creating.md`
- Editing a template or existing file: read `editing.md`

## Rules

- Run one command at a time.
- Use paragraph styles and spacing instead of empty paragraphs.
- Re-query after structural changes because paragraph and table indices shift.
- Use raw XML only when the high-level commands cannot express the needed footer, field, or formatting change.
