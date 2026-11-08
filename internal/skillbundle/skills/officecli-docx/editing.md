## Editing Existing `.docx` Files

1. Analyze the file first: issues, outline, text, styles, and specific elements.
2. Plan structural changes before content edits.
3. Remove or move elements from highest index to lowest when multiple items are involved.
4. Re-query after add/remove/move operations because indices change immediately.

### Common Commands

```bash
officecli view doc.docx issues
officecli view doc.docx outline
officecli view doc.docx text --max-lines 100
officecli get doc.docx /body --depth 1
officecli set doc.docx "/body/p[1]" --prop text="Updated Title"
officecli set doc.docx "/body/tbl[1]/tr[2]" --prop c1="Updated" --prop c2="$5.5M" --prop c3="$6.2M"
officecli validate doc.docx
```

### Key Pitfalls

- Structural changes shift indices; always inspect again before the next edit.
- Prefer existing styles over inline formatting so the document stays consistent.
- For advanced field/footer scenarios, raw XML may still be required.
