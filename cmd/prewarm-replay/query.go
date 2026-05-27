package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// cmdQuery synthesizes a wake from inline inputs. Unlike replay, it
// does not read any stored conversation metadata — the caller
// supplies the ChannelBinding fields and subjects directly. Best for
// "what if I had this subject?" iteration.
func cmdQuery(g *globals, args []string) error {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	message := fs.String("message", "", "user message text (required)")
	contactID := fs.String("contact-id", "", "ContactID for synthetic ChannelBinding")
	contactAddress := fs.String("contact-address", "", "Address (phone/email) for synthetic ChannelBinding")
	subjects := stringList{}
	fs.Var(&subjects, "subject", "extra subject to inject (repeatable; e.g. entity:light.office)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *message == "" && len(subjects) == 0 && *contactID == "" && *contactAddress == "" {
		return fmt.Errorf("query: at least one of -message, -subject, -contact-id, -contact-address is required")
	}

	s, err := openStores(g.DataDir)
	if err != nil {
		return err
	}
	defer s.close()

	var binding *memory.ChannelBinding
	if *contactID != "" || *contactAddress != "" {
		binding = &memory.ChannelBinding{
			ContactID: *contactID,
			Address:   *contactAddress,
		}
	}

	archive := memory.NewArchiveContextProvider(s.archive, g.MaxResults, g.MaxBytes, silentLogger())
	subjectP := knowledge.NewSubjectContextProvider(s.knowledge_, silentLogger())
	subjectP.SetMaxFacts(g.MaxFacts)

	res, err := runProviders(context.Background(), wake{
		ConversationID: "(synthetic)",
		UserMessage:    *message,
		Binding:        binding,
		ExtraSubjects:  []string(subjects),
	}, archive, subjectP)
	if err != nil {
		return err
	}

	return render(g.Format, []runResult{res})
}
