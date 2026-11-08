package promptbundle

// Status says how the prompt a type currently holds relates to the bundled one.
// The admin page shows it next to the prompt so "empty", "seeded" and "edited"
// stop looking the same from the outside.
type Status string

const (
	// StatusBundled: the stored text is the bundled text.
	StatusBundled Status = "bundled"
	// StatusStale: the stored text still hashes to the recorded baseline, so it
	// is an earlier seed rather than an edit — the next `prompts seed` replaces
	// it.
	StatusStale Status = "stale"
	// StatusEdited: no baseline, or a text that no longer hashes to it — an admin
	// owns this prompt.
	StatusEdited Status = "edited"
	// StatusEmpty: nothing is stored, so the runtime gets no role prompt at all.
	StatusEmpty Status = "empty"
	// StatusNone: this type ships no bundled prompt (admin-created types, and the
	// retired blueprints that some databases still hold).
	StatusNone Status = "none"
)

// Classify reports where a stored prompt stands relative to the bundle.
//
// The order of the checks mirrors SeedPlatformPrompts, because the answer has to
// agree with what the next seed run will do:
//
//	no bundle entry        → none
//	nothing stored         → empty
//	equals the bundle      → bundled
//	hashes to the baseline → stale (an unmodified earlier seed)
//	anything else          → edited
func Classify(storedText, storedHash string, bundled BundledPrompt, hasBundle bool) Status {
	switch {
	case !hasBundle:
		return StatusNone
	case storedText == "":
		return StatusEmpty
	case storedText == bundled.Text:
		return StatusBundled
	case storedHash != "" && storedHash == ContentHash(storedText):
		return StatusStale
	default:
		return StatusEdited
	}
}
