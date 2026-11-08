## Editing Existing `.pptx` Files

1. Analyze the template first with `outline`, `annotated`, and `get --depth 1`.
2. Perform structural changes before text, image, or chart edits.
3. Re-query after clone/remove/reorder/z-order operations because slide and shape indices change.

### Common Commands

```bash
officecli view template.pptx outline
officecli view template.pptx annotated
officecli get template.pptx '/slide[1]' --depth 1
officecli set template.pptx '/slide[1]/shape[1]' --prop text="New Title Text"
officecli set template.pptx '/slide[3]/chart[1]' --prop title="Updated Revenue Trend"
officecli set template.pptx '/slide[1]/shape[3]' --prop zorder=back
officecli validate template.pptx
```

### Key Pitfalls

- `view annotated` hides some non-text decorative shapes; use `get --depth 1` to see the full shape list.
- After z-order changes, shape indices can renumber immediately.
- Use visual QA with `view html --browser` before treating a deck as done.
