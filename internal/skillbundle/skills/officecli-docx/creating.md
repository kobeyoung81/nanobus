## Creating `.docx` Files

1. Create a blank document and set metadata first.
2. Define page setup, default font, and any heading styles before adding lots of content.
3. Build incrementally: headings, paragraphs, tables, charts, headers, and footers.
4. Use `officecli open <file>` / `officecli close <file>` for multi-step work.

### Common Commands

```bash
officecli create report.docx
officecli open report.docx
officecli set report.docx / --prop title="Q4 Report" --prop author="Team Alpha"
officecli set report.docx / --prop pageWidth=11906 --prop pageHeight=16838 --prop marginTop=1440 --prop marginBottom=1440 --prop marginLeft=1440 --prop marginRight=1440
officecli add report.docx /body --type paragraph --prop text="Executive Summary" --prop style=Heading1
officecli add report.docx /body --type paragraph --prop text="Revenue increased by 25% year-over-year."
officecli validate report.docx
officecli close report.docx
```

### Tips

- Add structural elements such as styles, headers, footers, TOC, and tables before fine-grained content edits.
- Use batch mode only for repetitive paragraph/table entry; keep structural commands separate.
- Re-run `officecli validate` before handing the document off.
