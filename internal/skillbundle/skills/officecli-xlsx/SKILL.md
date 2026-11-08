---
name: officecli-xlsx
description: Create, inspect, edit, and validate .xlsx workbooks with OfficeCLI.
metadata: {"nanobot":{"requires":{"bins":["officecli"]}}}
---

# OfficeCLI XLSX Skill

Use this skill any time an `.xlsx`, `.csv`, or spreadsheet-style workbook task is involved.

## Verify

```bash
officecli --version
```

## Read and Inspect

```bash
officecli view data.xlsx text
officecli view data.xlsx outline
officecli view data.xlsx annotated
officecli view data.xlsx issues
officecli get data.xlsx /
officecli get data.xlsx "/Sheet1/A1:D10"
officecli query data.xlsx 'cell:has(formula)'
officecli validate data.xlsx
```

## Routing

- Creating a workbook from scratch: read `creating.md`
- Editing an existing workbook: read `editing.md`

## Rules

- Use formulas for calculations instead of hardcoded result values.
- Re-check formula ranges after adding or removing rows.
- For multi-step edits, use resident mode with `open` and `close`.
- Verify cross-sheet formulas with `get` after setting them.
