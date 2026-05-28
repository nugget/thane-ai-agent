package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// cmdReplay loads a stored conversation's ChannelBinding and either
// the latest archived user message (default) or a caller-supplied
// message, then runs every prewarm provider against the result.
func cmdReplay(g *globals, args []string) error {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	convID := fs.String("conv-id", "", "conversation ID to replay (required)")
	message := fs.String("message", "", "override the user message (default: latest archived user message)")
	subjects := stringList{}
	fs.Var(&subjects, "subject", "extra subject to inject (repeatable; e.g. entity:light.office)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *convID == "" {
		return fmt.Errorf("replay: -conv-id is required")
	}

	s, err := openStores(g.DataDir)
	if err != nil {
		return err
	}
	defer s.close()

	binding, err := loadChannelBinding(s.thane, *convID)
	if err != nil {
		return err
	}

	userMsg := *message
	if userMsg == "" {
		latest, err := latestUserMessage(s.thane, *convID)
		if err != nil {
			return err
		}
		if latest == "" {
			return fmt.Errorf("replay: no user message found for %q and -message not supplied", *convID)
		}
		userMsg = latest
	}

	archive := memory.NewArchiveContextProvider(s.searcher(), g.MaxResults, g.MaxBytes, silentLogger())
	subjectP := knowledge.NewSubjectContextProvider(s.knowledge_, silentLogger())
	subjectP.SetMaxFacts(g.MaxFacts)

	res, err := runProviders(context.Background(), wake{
		ConversationID: *convID,
		UserMessage:    userMsg,
		Binding:        binding,
		ExtraSubjects:  []string(subjects),
	}, archive, subjectP)
	if err != nil {
		return err
	}

	return render(g.Format, []runResult{res})
}

// stringList implements flag.Value for repeatable -flag VALUE flags.
type stringList []string

func (s *stringList) String() string { return fmt.Sprint([]string(*s)) }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}
