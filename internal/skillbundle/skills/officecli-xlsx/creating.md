## Creating `.xlsx` Files

1. Create the workbook, set metadata, then add/rename sheets before filling them.
2. Build inputs, formulas, formatting, charts, validations, and named ranges incrementally.
3. Prefer formulas for computed cells so the workbook stays dynamic.

### Common Commands

```bash
officecli create data.xlsx
officecli open data.xlsx
officecli set data.xlsx / --prop title="Q4 Financial Report" --prop author="Finance Team"
officecli add data.xlsx / --type sheet --prop name="Revenue"
officecli set data.xlsx /Revenue/A1 --prop value=Month --prop bold=true
officecli set data.xlsx /Revenue/B2 --prop value=42000 --prop font.color=0000FF --prop numFmt='$#,##0'
officecli set data.xlsx /Revenue/C2 --prop formula="SUM(A2:B2)"
officecli validate data.xlsx
officecli close data.xlsx
```

### Tips

- Set column widths, freeze panes, and number formats explicitly.
- Use data validation on input cells where the workbook will be edited by humans.
- If you need many cell writes, batch them after the workbook structure exists.
