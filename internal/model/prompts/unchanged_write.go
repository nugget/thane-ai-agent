package prompts

// UnchangedWrite is the warning a document write returns when the document
// it would save is byte for byte the one already stored, so no revision was
// created. It speaks of the saved document, not of what the author sent,
// because Go may rewrite what was sent before comparing (a projection
// shortened to fit its budget, folded glyphs, canonical citations), and a
// rewritten value can land on the stored text although the author's own did
// not match it. lastChanged is when the document last did change, already
// rendered as a delta ("-40h2m"), or empty when that is unknown. The write
// succeeds and every counter reads it as a success, so a loop shown its own
// last publish can resend it for as long as nothing else breaks the habit;
// this is where the author learns it did.
func UnchangedWrite(lastChanged string) string {
	warning := "unchanged: the document this write would save is byte for byte the one already stored, so no new revision was created"
	if lastChanged != "" {
		warning += " and it still reads as last changed " + lastChanged
	}
	return warning + ". That is right only when nothing you have observed since then changes what it says. Text carried over from the copy you were shown is not a fresh judgment: compose from what you read this time."
}
