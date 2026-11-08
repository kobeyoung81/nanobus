## Creating `.pptx` Files

1. Create the deck, set metadata, and open it in resident mode.
2. Build slide-by-slide with explicit layouts, backgrounds, typography, and spacing.
3. Include visual elements such as charts, cards, circles, or stat callouts instead of text-only slides.

### Common Commands

```bash
officecli create slides.pptx
officecli open slides.pptx
officecli set slides.pptx / --prop title="Q4 Report" --prop author="Team Alpha"
officecli add slides.pptx / --type slide --prop layout=blank --prop background=1E2761
officecli add slides.pptx '/slide[1]' --type shape --prop text="Quarterly Business Review" --prop x=2cm --prop y=5cm --prop width=29.87cm --prop height=4cm --prop font=Georgia --prop size=44 --prop bold=true --prop color=FFFFFF --prop align=center --prop fill=none
officecli add slides.pptx '/slide[1]' --type chart --prop chartType=column --prop categories="Q1,Q2,Q3,Q4" --prop series1="Revenue:42,58,65,78" --prop x=2cm --prop y=9cm --prop width=20cm --prop height=8cm
officecli validate slides.pptx
officecli close slides.pptx
```

### Tips

- Keep body text at readable presentation sizes.
- Vary layouts across the deck instead of repeating one card grid or bullet slide.
- Use notes for speaker guidance on content-heavy slides.
