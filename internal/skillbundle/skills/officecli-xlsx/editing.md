## Editing Existing `.xlsx` Files

1. Inspect issues, outline, annotated content, formulas, charts, and named ranges first.
2. Complete structural changes before formula or formatting edits.
3. Re-query after row, sheet, or chart changes because references may shift.

### Common Commands

```bash
officecli view data.xlsx issues
officecli view data.xlsx outline
officecli get data.xlsx "/Revenue/B2"
officecli set data.xlsx "/Revenue/B5" --prop value=12500
officecli set data.xlsx "/Revenue/B14" --prop formula="SUM(B2:B13)"
officecli set data.xlsx "/Revenue/chart[1]" --prop title="Updated Revenue"
officecli validate data.xlsx
```

### Key Pitfalls

- Re-check formula ranges after inserted/removed rows.
- Cross-sheet formulas using `Sheet!Cell` syntax should be verified with `officecli get` after setting them.
- Do not overwrite formula cells with static values unless the user explicitly wants that.
