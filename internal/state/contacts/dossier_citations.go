package contacts

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// archiveSessionCitationPattern matches an archive-session citation in
// either separator form. The id is runs of letters and digits joined by
// single hyphens, so a hyphen that ends the id, or a doubled hyphen used
// as a dash, stays part of the surrounding prose.
var archiveSessionCitationPattern = regexp.MustCompile(`archive:session([:-])([[:alnum:]]+(?:-[[:alnum:]]+)*)?`)

// archiveSessionCitationPrefix begins every canonical archive-session
// citation.
const archiveSessionCitationPrefix = "archive:session:"

// sessionIDHexDigits is the number of hex digits in a full session id.
const sessionIDHexDigits = 32

// dossierCitationRecovery closes every citation refusal. Content search
// is the one lookup that can pick between sessions sharing a prefix, and
// evidence that cannot be pinned to a session still has an honest home.
const dossierCitationRecovery = "Cite archive evidence as archive:session:<full-session-uuid>. " +
	"To find a full id, search archive_search for the claim's own words: every hit carries its full session_id. " +
	"Evidence you cannot pin to one session belongs under ### Open Questions in full, not in prose that describes a prefix or a legacy session."

// dossierCitationRefusalMaxBytes is the ceiling on a whole citation
// refusal: the 16 KB AGENTS.md gives a tool result that is not a
// transcript. The refusal reaches the model as contact_dossier_write's
// entire result, so nothing else is competing for the budget.
const dossierCitationRefusalMaxBytes = 16 << 10

// dossierCitationListMaxBytes is what the itemized part of a refusal may
// spend, the ceiling less room for the sentence that introduces the
// list, dossierCitationRecovery, and the frame a faceted write wraps a
// refusal in. Reserving that room is what keeps the recovery inside the
// cap instead of pushed past it by a long list.
const dossierCitationListMaxBytes = dossierCitationRefusalMaxBytes - (1 << 10)

// citationKind classifies one archive-session citation as written.
type citationKind int

const (
	// citationCanonical is archive:session:<full lowercase uuid>.
	citationCanonical citationKind = iota
	// citationRespelled names a whole session id in another spelling:
	// the retired hyphen separator, uppercase hex, or displaced hyphens.
	citationRespelled
	// citationLeadingPart carries fewer than all 32 hex digits.
	citationLeadingPart
	// citationMalformed cannot begin any session id.
	citationMalformed
)

// sessionCitation is one archive-session citation found in a projection.
type sessionCitation struct {
	// text is the citation exactly as written.
	text      string
	separator string
	kind      citationKind
	// id is the full id or leading part in canonical 8-4-4-4-12 form.
	id string
	// digits counts the hex digits the citation carries.
	digits int
	// problem says why a malformed citation names no session.
	problem string
}

// canonical returns the canonical citation for a full id.
func (c sessionCitation) canonical() string {
	return archiveSessionCitationPrefix + c.id
}

// classifySessionCitation reads one match of
// [archiveSessionCitationPattern].
func classifySessionCitation(match []string) sessionCitation {
	c := sessionCitation{text: match[0], separator: match[1]}
	raw := match[2]
	hex := make([]byte, 0, sessionIDHexDigits)
	for _, r := range raw {
		switch {
		case r == '-':
		case '0' <= r && r <= '9', 'a' <= r && r <= 'f':
			hex = append(hex, byte(r))
		case 'A' <= r && r <= 'F':
			hex = append(hex, byte(r-'A'+'a'))
		default:
			c.kind = citationMalformed
			c.problem = fmt.Sprintf("%q is not a hex digit", r)
			return c
		}
	}
	c.digits = len(hex)
	switch {
	case c.digits == 0:
		c.kind = citationMalformed
		c.problem = "no session id follows the separator"
		return c
	case c.digits > sessionIDHexDigits:
		c.kind = citationMalformed
		c.problem = fmt.Sprintf("it has %d hex digits and a session id has %d", c.digits, sessionIDHexDigits)
		return c
	}
	c.id = hyphenateSessionID(hex)
	switch {
	case c.digits < sessionIDHexDigits:
		c.kind = citationLeadingPart
	case uuid.MustParse(c.id) == uuid.Nil:
		c.kind = citationMalformed
		c.problem = "the all-zero id names no session"
	case c.separator == ":" && raw == c.id:
		c.kind = citationCanonical
	default:
		c.kind = citationRespelled
	}
	return c
}

// hyphenateSessionID renders hex digits with hyphens at the canonical
// 8-4-4-4-12 positions, for a full id or any leading part of one.
func hyphenateSessionID(hex []byte) string {
	var b strings.Builder
	b.Grow(len(hex) + 4)
	for i, d := range hex {
		if i == 8 || i == 12 || i == 16 || i == 20 {
			b.WriteByte('-')
		}
		b.WriteByte(d)
	}
	return b.String()
}

// dossierProjection is one authored projection, addressable so a
// citation rewrite can replace its text in place.
type dossierProjection struct {
	name  string
	value *string
}

func dossierProjections(payload *documentfacets.Payload) []dossierProjection {
	return []dossierProjection{
		{name: "status_line", value: &payload.StatusLine},
		{name: "teaser", value: &payload.Teaser},
		{name: "digest", value: &payload.Digest},
		{name: "full", value: &payload.Full},
	}
}

// canonicalizedCitation reports one citation Go respelled before writing.
type canonicalizedCitation struct {
	From   string   `json:"from"`
	To     string   `json:"to"`
	Fields []string `json:"fields"`
}

// canonicalizeDossierCitations rewrites, in place, every citation that
// names a whole session id in a non-canonical spelling to
// archive:session:<uuid>, and reports each rewrite. The text already
// names the session, so no archive lookup is involved. A leading part is
// left as written for [validateDossierEvidenceCitations] to refuse:
// completing it would be a claim about which session the author meant.
func canonicalizeDossierCitations(payload *documentfacets.Payload) []canonicalizedCitation {
	var changes []canonicalizedCitation
	index := make(map[string]int)
	for _, field := range dossierProjections(payload) {
		*field.value = archiveSessionCitationPattern.ReplaceAllStringFunc(*field.value, func(text string) string {
			c := classifySessionCitation(archiveSessionCitationPattern.FindStringSubmatch(text))
			if c.kind != citationRespelled {
				return text
			}
			i, seen := index[text]
			if !seen {
				i = len(changes)
				index[text] = i
				changes = append(changes, canonicalizedCitation{From: text, To: c.canonical()})
			}
			if !slices.Contains(changes[i].Fields, field.name) {
				changes[i].Fields = append(changes[i].Fields, field.name)
			}
			return c.canonical()
		})
	}
	return changes
}

// citationProblem is one distinct refused citation and the projections
// that carry it.
type citationProblem struct {
	citation sessionCitation
	fields   []string
}

// validateDossierEvidenceCitations keeps archive claims independently
// checkable: every archive-session citation must be
// archive:session:<full canonical uuid>. It checks the text only; whether
// the session still exists is deliberately not asked, because a re-import
// gives imported sessions new ids and would otherwise make every dossier
// citing them unwritable. resolve, when set, lets a refused leading part
// name its full citation or its candidates; the root validator passes
// nil and refuses without them, so ctx bounds nothing on that path.
func validateDossierEvidenceCitations(ctx context.Context, payload documentfacets.Payload, resolve ArchiveSessionResolver) error {
	var problems []*citationProblem
	byText := make(map[string]*citationProblem)
	var rejected []string
	for _, field := range dossierProjections(&payload) {
		fieldRejected := false
		for _, match := range archiveSessionCitationPattern.FindAllStringSubmatch(*field.value, -1) {
			c := classifySessionCitation(match)
			if c.kind == citationCanonical {
				continue
			}
			fieldRejected = true
			problem, seen := byText[c.text]
			if !seen {
				problem = &citationProblem{citation: c}
				byText[c.text] = problem
				problems = append(problems, problem)
			}
			if !slices.Contains(problem.fields, field.name) {
				problem.fields = append(problem.fields, field.name)
			}
		}
		if fieldRejected {
			rejected = append(rejected, field.name)
		}
	}
	if len(problems) == 0 {
		return nil
	}

	// One now for the whole refusal: every candidate age it renders is
	// measured from the same instant, however many lookups it makes.
	resolver := newCitationResolver(resolve, time.Now())
	lines := make([]string, 0, len(problems))
	for _, problem := range problems {
		lines = append(lines, describeCitationProblem(ctx, problem, resolver))
	}
	// The list is what grows with the dossier: ten leading parts, each
	// listing five candidates with their titles. Bounding it here rather
	// than the finished error keeps dossierCitationRecovery on the end,
	// which is the one sentence the model needs whatever got dropped.
	return toolargs.Rejected(fmt.Errorf("archive-session citations must each name one archived session by its full id:%s\n%s",
		boundedRefusalList(lines, dossierCitationListMaxBytes), dossierCitationRecovery), rejected...)
}

// describeCitationProblem says what is wrong with one refused citation
// and, for a leading part, what the archive knows about it.
func describeCitationProblem(ctx context.Context, problem *citationProblem, resolver *citationResolver) string {
	c := problem.citation
	// The citation is echoed through the same bound as every other
	// caller-supplied value this package quotes back. A respelled or
	// partial id is well under it, but a malformed one is only as short
	// as whatever the pattern matched: the id it accepts is an unbounded
	// run of alphanumerics joined by hyphens, so a pasted token or a
	// mangled slug in the projection would otherwise size the refusal.
	subject := echoForRefusal(c.text) + " " + fieldPhrase(problem.fields)
	switch c.kind {
	case citationRespelled:
		if c.separator == "-" {
			return fmt.Sprintf("%s uses the retired hyphen separator; write it as %s", subject, c.canonical())
		}
		return fmt.Sprintf("%s spells the session id in a non-canonical form; write it as %s", subject, c.canonical())
	case citationLeadingPart:
		return fmt.Sprintf("%s carries only the first %d of a session id's %d hex digits; a leading part is not a durable citation because sessions imported together share leading digits%s",
			subject, c.digits, sessionIDHexDigits, resolver.describe(ctx, c.id))
	default:
		return fmt.Sprintf("%s is not a session id: %s", subject, c.problem)
	}
}

// fieldPhrase names the projections a citation appears in.
func fieldPhrase(fields []string) string {
	if len(fields) == 1 {
		return "in field " + fields[0]
	}
	return "in fields " + strings.Join(fields[:len(fields)-1], ", ") + " and " + fields[len(fields)-1]
}
