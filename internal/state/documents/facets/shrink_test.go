package facets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// shrinkReply scripts one call of a fake Shrink.
type shrinkReply func(ctx context.Context, text string, limit int) (string, error)

func reply(text string) shrinkReply {
	return func(context.Context, string, int) (string, error) { return text, nil }
}

func replyErr(err error) shrinkReply {
	return func(context.Context, string, int) (string, error) { return "", err }
}

// shrinkCall records what one call of a fake Shrink received.
type shrinkCall struct {
	text     string
	limit    int
	deadline time.Duration
}

// scriptedShrinker returns a Shrinker whose Shrink plays replies in order
// (the last one repeats), with every call recorded and every log record
// captured as JSON.
func scriptedShrinker(replies ...shrinkReply) (*Shrinker, *[]shrinkCall, *bytes.Buffer) {
	calls := &[]shrinkCall{}
	logs := &bytes.Buffer{}
	return &Shrinker{
		Model:  "fast-model",
		Logger: slog.New(slog.NewJSONHandler(logs, nil)),
		Shrink: func(ctx context.Context, text string, limit int) (string, error) {
			call := shrinkCall{text: text, limit: limit}
			if deadline, ok := ctx.Deadline(); ok {
				call.deadline = time.Until(deadline)
			}
			*calls = append(*calls, call)
			return replies[min(len(*calls), len(replies))-1](ctx, text, limit)
		},
	}, calls, logs
}

// writerValidate stands in for a writer's full validation: the contract,
// plus an optional domain rule charged to status_line, framed as one
// refusal. It records every refusal it returns so a test can check Fit
// hands back the first one itself.
func writerValidate(domain func(Payload) error) (func(Payload) error, *[]error) {
	refusals := &[]error{}
	return func(p Payload) error {
		var violations []error
		if err := DefaultContract().Validate(p); err != nil {
			violations = append(violations, err)
		}
		if domain != nil {
			if err := domain(p); err != nil {
				violations = append(violations, toolargs.Rejected(err, "status_line"))
			}
		}
		if len(violations) == 0 {
			return nil
		}
		refusal := InvalidProjectionsError("test projections", violations...)
		*refusals = append(*refusals, refusal)
		return refusal
	}, refusals
}

func validShrinkPayload() Payload {
	return Payload{StatusLine: "Current.", Teaser: "Open for the hook.", Digest: "Enough to act on.", Full: "Complete detail."}
}

// assertRefusedUnchanged checks Fit returned the writer's first refusal
// itself and the payload it was given.
func assertRefusedUnchanged(t *testing.T, got Payload, shortened []Shortened, err error, sent Payload, refusals []error) {
	t.Helper()
	if len(refusals) == 0 || err != refusals[0] {
		t.Fatalf("Fit() error = %v, want the original refusal %v", err, refusals)
	}
	if got != sent || shortened != nil {
		t.Fatalf("Fit() = %+v, %v; want the sent payload and nothing shortened", got, shortened)
	}
}

// TestFitShrinksOnlyOverrunsWithinHalfTheLimit generates every budgeted
// projection at three overages — one rune over, exactly half the limit over
// (the last overage that shrinks), and one past that — and checks only the
// first two reach the shrink model. Overruns are counted in runes, so the
// generated text is multi-byte.
func TestFitShrinksOnlyOverrunsWithinHalfTheLimit(t *testing.T) {
	t.Parallel()

	for _, field := range DefaultContract().Fields() {
		if field.MaxRunes == 0 {
			continue
		}
		for _, overage := range []int{1, field.MaxRunes / 2, field.MaxRunes/2 + 1} {
			wantShrink := overage <= field.MaxRunes/2
			t.Run(fmt.Sprintf("%s %d over", field.Key, overage), func(t *testing.T) {
				t.Parallel()
				sent := validShrinkPayload()
				item, _ := sectionByKey(field.Key)
				*item.value(&sent) = strings.Repeat("é", field.MaxRunes+overage)
				fits := strings.Repeat("y", field.MaxRunes)
				shrinker, calls, _ := scriptedShrinker(reply(fits))
				validate, refusals := writerValidate(nil)

				got, shortened, err := shrinker.Fit(context.Background(), sent, validate)
				if !wantShrink {
					assertRefusedUnchanged(t, got, shortened, err, sent, *refusals)
					if len(*calls) != 0 {
						t.Fatalf("shrink called %d times for an overage past half the limit", len(*calls))
					}
					return
				}
				if err != nil {
					t.Fatalf("Fit() error = %v, want the shortened payload", err)
				}
				if len(*calls) != 1 || (*calls)[0].limit != field.MaxRunes || (*calls)[0].text != *item.value(&sent) {
					t.Fatalf("shrink calls = %+v, want one with the sent text and limit %d", *calls, field.MaxRunes)
				}
				if value := *item.value(&got); value != fits {
					t.Fatalf("%s = %q, want the shortened text", field.Key, value)
				}
				want := Shortened{Projection: field.Key, Limit: field.MaxRunes, SentCharacters: field.MaxRunes + overage, SavedCharacters: field.MaxRunes, SavedText: fits}
				if len(shortened) != 1 || shortened[0].Projection != want.Projection || shortened[0].Limit != want.Limit ||
					shortened[0].SentCharacters != want.SentCharacters || shortened[0].SavedCharacters != want.SavedCharacters || shortened[0].SavedText != want.SavedText {
					t.Fatalf("shortened = %+v, want %+v", shortened, want)
				}
			})
		}
	}
}

// TestFitAcceptsOnlyAShrinkThatFits pins what a shrink result must be to
// replace the projection — non-empty, within the limit, single-line where
// the projection requires it — within two calls, and that the full
// validation still decides afterwards.
func TestFitAcceptsOnlyAShrinkThatFits(t *testing.T) {
	t.Parallel()

	overStatus := strings.Repeat("s", statusLineMaxRunes+22)
	fits := strings.Repeat("f", statusLineMaxRunes)
	tests := []struct {
		name      string
		mutate    func(*Payload)
		replies   []shrinkReply
		domain    func(Payload) error
		wantCalls int
		// want maps each projection key to its saved text; nil wants the
		// original refusal.
		want map[string]string
	}{
		{name: "fits on the first call", replies: []shrinkReply{reply(fits)}, wantCalls: 1, want: map[string]string{"status_line": fits}},
		{name: "surrounding whitespace is trimmed", replies: []shrinkReply{reply("  Shorter.\n")}, wantCalls: 1, want: map[string]string{"status_line": "Shorter."}},
		{name: "still over, then fits", replies: []shrinkReply{reply(fits + "!"), reply(fits)}, wantCalls: 2, want: map[string]string{"status_line": fits}},
		{name: "an error, then fits", replies: []shrinkReply{replyErr(errors.New("upstream 503")), reply(fits)}, wantCalls: 2, want: map[string]string{"status_line": fits}},
		{name: "still over twice", replies: []shrinkReply{reply(fits + "!")}, wantCalls: 2},
		{name: "multi-line on a single-line projection", replies: []shrinkReply{reply("one\ntwo")}, wantCalls: 2},
		{name: "empty, then only whitespace", replies: []shrinkReply{reply(""), reply(" \n ")}, wantCalls: 2},
		{name: "an error twice", replies: []shrinkReply{replyErr(errors.New("upstream 503"))}, wantCalls: 2},
		{
			name:    "multi-line on a projection that allows it",
			mutate:  func(p *Payload) { p.StatusLine = "Current."; p.Teaser = strings.Repeat("t", teaserMaxRunes+40) },
			replies: []shrinkReply{reply("Why open it:\nthe hook.")}, wantCalls: 1,
			want: map[string]string{"teaser": "Why open it:\nthe hook."},
		},
		{
			name:    "every small overrun is shortened",
			mutate:  func(p *Payload) { p.Teaser = strings.Repeat("t", teaserMaxRunes+40) },
			replies: []shrinkReply{reply("Short.")}, wantCalls: 2,
			want: map[string]string{"status_line": "Short.", "teaser": "Short."},
		},
		{
			name:    "the shortened text breaks another contract rule",
			replies: []shrinkReply{reply("## Details")}, wantCalls: 1,
		},
		{
			name:    "the shortened text breaks a writer's own rule",
			domain:  func(p Payload) error { return mentions(p.StatusLine, "Alice") },
			replies: []shrinkReply{reply("Alice is current.")}, wantCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sent := validShrinkPayload()
			sent.StatusLine = overStatus
			if tt.mutate != nil {
				tt.mutate(&sent)
			}
			shrinker, calls, _ := scriptedShrinker(tt.replies...)
			validate, refusals := writerValidate(tt.domain)

			got, shortened, err := shrinker.Fit(context.Background(), sent, validate)
			if len(*calls) != tt.wantCalls {
				t.Fatalf("shrink calls = %d, want %d", len(*calls), tt.wantCalls)
			}
			if tt.want == nil {
				assertRefusedUnchanged(t, got, shortened, err, sent, *refusals)
				return
			}
			if err != nil {
				t.Fatalf("Fit() error = %v, want the shortened payload", err)
			}
			if len(shortened) != len(tt.want) {
				t.Fatalf("shortened = %+v, want %d projections", shortened, len(tt.want))
			}
			for key, text := range tt.want {
				if value, _ := got.ByKey(key); value != text {
					t.Errorf("%s = %q, want %q", key, value, text)
				}
			}
		})
	}
}

func mentions(text, name string) error {
	if strings.Contains(text, name) {
		return fmt.Errorf("status_line names %s", name)
	}
	return nil
}

// TestFitBoundsEachShrinkCall pins that every call runs on its own deadline
// of a few seconds, and that a call that runs into it costs the write
// nothing but the original refusal.
func TestFitBoundsEachShrinkCall(t *testing.T) {
	t.Parallel()

	sent := validShrinkPayload()
	sent.StatusLine = strings.Repeat("s", statusLineMaxRunes+5)
	waits := func(ctx context.Context, _ string, _ int) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	shrinker, calls, logs := scriptedShrinker(waits)
	validate, refusals := writerValidate(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	got, shortened, err := shrinker.Fit(ctx, sent, validate)
	assertRefusedUnchanged(t, got, shortened, err, sent, *refusals)
	if len(*calls) != shrinkAttempts {
		t.Fatalf("shrink calls = %d, want %d", len(*calls), shrinkAttempts)
	}
	if !strings.Contains(logs.String(), context.DeadlineExceeded.Error()) {
		t.Errorf("log = %s, want the deadline as the reason", logs)
	}

	shrinker, calls, _ = scriptedShrinker(reply("Short."))
	if _, _, err := shrinker.Fit(context.Background(), sent, validate); err != nil {
		t.Fatalf("Fit() error = %v", err)
	}
	if d := (*calls)[0].deadline; d <= 0 || d > 5*time.Second {
		t.Fatalf("shrink call deadline = %v, want one of at most five seconds", d)
	}
}

// TestFitLeavesAnyOtherViolationToTheModel pins that one violation of any
// other kind, or one overrun past half its limit, keeps every overrun away
// from the shrink model and returns the refusal the writer built.
func TestFitLeavesAnyOtherViolationToTheModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Payload)
		domain func(Payload) error
	}{
		{name: "a required projection left empty", mutate: func(p *Payload) { p.Digest = " " }},
		{name: "a reserved heading in another projection", mutate: func(p *Payload) { p.Teaser = "## Digest\n\nsmuggled" }},
		{name: "a line break in the over-budget projection", mutate: func(p *Payload) { p.StatusLine += "\nmore" }},
		{name: "a second overrun past half its limit", mutate: func(p *Payload) { p.Teaser = strings.Repeat("t", teaserMaxRunes*3/2+1) }},
		{name: "a writer's own rule", domain: func(Payload) error { return errors.New("status_line repeats the subject name") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sent := validShrinkPayload()
			sent.StatusLine = strings.Repeat("s", statusLineMaxRunes+10)
			if tt.mutate != nil {
				tt.mutate(&sent)
			}
			shrinker, calls, _ := scriptedShrinker(reply("Short."))
			validate, refusals := writerValidate(tt.domain)

			got, shortened, err := shrinker.Fit(context.Background(), sent, validate)
			assertRefusedUnchanged(t, got, shortened, err, sent, *refusals)
			if len(*calls) != 0 {
				t.Fatalf("shrink called %d times beside another violation", len(*calls))
			}
		})
	}
}

// TestFitWithoutAShrinkerIsTheWritersValidation pins the off switch: a nil
// Shrinker, or one with no Shrink func, returns exactly what the writer's
// validation returned, and a valid payload never reaches a shrink model.
func TestFitWithoutAShrinkerIsTheWritersValidation(t *testing.T) {
	t.Parallel()

	over := validShrinkPayload()
	over.StatusLine = strings.Repeat("s", statusLineMaxRunes+1)
	for name, shrinker := range map[string]*Shrinker{"nil shrinker": nil, "no shrink func": {Model: "fast-model"}} {
		validate, refusals := writerValidate(nil)
		got, shortened, err := shrinker.Fit(context.Background(), over, validate)
		if len(*refusals) != 1 {
			t.Fatalf("%s: validate ran %d times, want once", name, len(*refusals))
		}
		assertRefusedUnchanged(t, got, shortened, err, over, *refusals)
	}

	shrinker, calls, _ := scriptedShrinker(reply("Short."))
	validate, _ := writerValidate(nil)
	got, shortened, err := shrinker.Fit(context.Background(), validShrinkPayload(), validate)
	if err != nil || got != validShrinkPayload() || shortened != nil || len(*calls) != 0 {
		t.Fatalf("Fit(valid) = %+v, %v, %v with %d calls; want it unchanged", got, shortened, err, len(*calls))
	}
}

// logPrefix wants a log value that starts with it, for a reason that quotes
// a whole refusal.
type logPrefix string

// TestFitLogsEveryShrinkForTheOperator pins what the operator keeps, since
// choosing a shrink model needs the replies it rejected as much as the ones
// it saved: every reply not used, with its text when it had one and why, and
// one outcome per projection a shrink touched. A landed write logs each
// saved text; when the original refusal stands, each projection logs the
// text it had been shortened to, if any, and the reason.
func TestFitLogsEveryShrinkForTheOperator(t *testing.T) {
	t.Parallel()

	const (
		saved    = "projection shortened to fit its budget"
		rejected = "projection shrink reply rejected"
		failed   = "projection shrink failed; the original refusal stands"
	)
	over := strings.Repeat("s", statusLineMaxRunes+22)
	stillOver := strings.Repeat("r", statusLineMaxRunes+10)
	overTeaser := strings.Repeat("t", teaserMaxRunes+40)
	for _, tt := range []struct {
		name    string
		mutate  func(*Payload)
		replies []shrinkReply
		want    []map[string]any
	}{
		{
			name:    "shortened",
			replies: []shrinkReply{reply("Short.")},
			want: []map[string]any{{
				"level": "INFO", "msg": saved, "facet": "status_line", "limit": 120.0, "original_runes": 142.0, "model": "fast-model",
				"attempts": 1.0, "original_text": over, "new_runes": 6.0, "new_text": "Short.", "reason": nil,
			}},
		},
		{
			name:    "every reply the model gave",
			replies: []shrinkReply{reply(stillOver), replyErr(errors.New("upstream 503"))},
			want: []map[string]any{
				{"msg": rejected, "facet": "status_line", "attempts": 1.0, "new_runes": 130.0, "new_text": stillOver, "reason": "the shortened text is 130 characters, still over 120"},
				{"msg": rejected, "facet": "status_line", "attempts": 2.0, "new_text": nil, "reason": "upstream 503"},
				{"msg": failed, "facet": "status_line", "attempts": 2.0, "original_text": over, "new_text": nil, "reason": "status_line was not shortened: upstream 503"},
			},
		},
		{
			name:    "a later projection fails",
			mutate:  func(p *Payload) { p.Teaser = overTeaser },
			replies: []shrinkReply{reply("Status OK."), replyErr(errors.New("upstream 503"))},
			want: []map[string]any{
				{"msg": rejected, "facet": "teaser", "attempts": 1.0, "reason": "upstream 503"},
				{"msg": rejected, "facet": "teaser", "attempts": 2.0, "reason": "upstream 503"},
				{"msg": failed, "facet": "status_line", "attempts": 1.0, "original_text": over, "new_runes": 10.0, "new_text": "Status OK.", "reason": "teaser was not shortened: upstream 503"},
				{"msg": failed, "facet": "teaser", "attempts": 2.0, "original_text": overTeaser, "new_text": nil, "reason": "teaser was not shortened: upstream 503"},
			},
		},
		{
			name:    "the shortened payload fails validation",
			replies: []shrinkReply{reply("## Details")},
			want: []map[string]any{
				{"msg": failed, "facet": "status_line", "attempts": 1.0, "new_text": "## Details", "reason": logPrefix("the shortened payload failed validation: test projections are invalid")},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sent := validShrinkPayload()
			sent.StatusLine = over
			if tt.mutate != nil {
				tt.mutate(&sent)
			}
			shrinker, _, logs := scriptedShrinker(tt.replies...)
			validate, _ := writerValidate(nil)
			_, _, _ = shrinker.Fit(context.Background(), sent, validate)
			var records []map[string]any
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var record map[string]any
				if err := json.Unmarshal([]byte(line), &record); err != nil {
					t.Fatalf("log line %q is not JSON: %v", line, err)
				}
				records = append(records, record)
			}
			if len(records) != len(tt.want) {
				t.Fatalf("log = %s, want %d records", logs, len(tt.want))
			}
			for i, want := range tt.want {
				for key, value := range want {
					if prefix, ok := value.(logPrefix); ok {
						if got, _ := records[i][key].(string); !strings.HasPrefix(got, string(prefix)) {
							t.Errorf("record %d %s = %q, want it to start %q", i, key, got, prefix)
						}
						continue
					}
					if records[i][key] != value {
						t.Errorf("record %d %s = %v, want %v", i, key, records[i][key], value)
					}
				}
			}
		})
	}
}
