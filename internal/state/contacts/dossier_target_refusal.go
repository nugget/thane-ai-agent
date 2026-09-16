package contacts

import "errors"

// ErrDossierTargetRefused matches, through errors.Is, a
// contact_dossier_write refusal of the contact a call named rather than
// of the content it carried: the contact_id names no active contact, or
// the contact's first dossier would be a second dossier for a person
// another record already holds one for. No retry of that contact_id is
// the write the model meant — the refusal points it at another
// contact_id, or at the operator — so a caller that judges a wake's
// writes per contact must not wait for that contact_id to land.
//
// A contact_id that is not a canonical UUID is not such a refusal: the
// contact it spells is unambiguous, and the canonical retry lands the
// same dossier.
var ErrDossierTargetRefused = errors.New("contact_dossier_write refused the contact the call named")

// targetRefusal marks err as an [ErrDossierTargetRefused] without
// changing the text the model reads.
type targetRefusal struct{ err error }

func (r targetRefusal) Error() string   { return r.err.Error() }
func (r targetRefusal) Unwrap() []error { return []error{r.err, ErrDossierTargetRefused} }
