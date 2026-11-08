---
name: mdtopdf
description: Convert Markdown files to PDF with themes, TOC generation, page settings, and optional footer metadata.
metadata: {"nanobot":{"requires":{"bins":["md2pdf"]}}}
---

# md2pdf Skill

Use this skill when the user wants Markdown rendered as a PDF.

## Verify

```bash
md2pdf -version
```

## Quick Start

```bash
md2pdf -i input.md -o output.pdf
```

## Common Flags

- `--theme light|dark|/path/to/theme.json`
- `--generate-toc`
- `--page-size A4|A3|A5`
- `--orientation portrait|landscape`
- `--new-page-on-hr`
- `--with-footer --author "Name" --title "Document Title"`
- `--font-file ... --font-name ... --unicode-encoding ...`

## Workflow

1. Read the source Markdown first so you know whether it contains tables, images, code blocks, or non-ASCII text.
2. Write the output PDF to a clear workspace path.
3. If the user wants a table of contents, pass `--generate-toc`.
4. If the document needs a specific look, use `--theme` with `light`, `dark`, or a custom JSON theme file.
5. If the document contains non-Latin text, pair `--unicode-encoding` with a matching font file and font name.

## Examples

```bash
md2pdf -i report.md -o report.pdf --theme dark --generate-toc
md2pdf -i slides.md -o slides.pdf --new-page-on-hr --orientation landscape
md2pdf -i handbook.md -o handbook.pdf --with-footer --author "Team Alpha" --title "Handbook"
```

## Notes

- The executable is `md2pdf`.
- Input can be a Markdown file, a directory of Markdown files, or an HTTP(S) URL.
- Raw HTML in Markdown is not rendered as HTML; it is treated like code-block content.
