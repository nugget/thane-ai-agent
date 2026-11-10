package prompts

// UnchangedWrite is the warning a document write returns when everything
// it sent matches the stored document byte for byte, so no revision was
// created. lastChanged is when the document last did change, already
// rendered as a delta ("-40h2m"), or empty when that is unknown. The write
// succeeds and every counter reads it as a success, so a loop shown its
// own last publish can resend it for as long as nothing else breaks the
// habit; this is where the author learns it did.
func UnchangedWrite(lastChanged string) string {
	warning := "unchanged: everything you sent matches the stored document byte for byte, so no new revision was created"
	if lastChanged != "" {
		warning += " and it still reads as last changed " + lastChanged
	}
	return warning + ". That is right only when nothing you have observed since then changes what it says. Text carried over from the copy you were shown is not a fresh judgment: compose from what you read this time."
}
