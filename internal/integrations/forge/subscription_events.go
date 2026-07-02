package forge

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/checkout"
)

func subscriptionMetadata(sub ProjectSubscription) map[string]string {
	metadata := map[string]string{
		"subscription_id": sub.ID,
		"account":         sub.Account,
		"repo":            sub.Repo,
	}
	if sub.Branch != "" {
		metadata["branch"] = sub.Branch
	}
	if sub.Name != "" {
		metadata["name"] = sub.Name
	}
	if sub.CheckoutPath != "" {
		metadata["local_checkout"] = sub.CheckoutPath
	}
	if sub.LastSyncedSHA != "" {
		metadata["last_synced_sha"] = sub.LastSyncedSHA
	}
	return metadata
}

func annotateSubscriptionEvents(events []messages.LoopEventPayload, sub ProjectSubscription) {
	for i := range events {
		if events[i].Metadata == nil {
			events[i].Metadata = map[string]string{}
		}
		if sub.CheckoutPath != "" {
			events[i].Metadata["local_checkout"] = sub.CheckoutPath
		}
		if sub.LastSyncedSHA != "" {
			events[i].Metadata["last_synced_sha"] = sub.LastSyncedSHA
		}
	}
}

type mirrorSubscriptionCheckoutSyncer struct {
	logger *slog.Logger
}

func (s mirrorSubscriptionCheckoutSyncer) Sync(ctx context.Context, sub ProjectSubscription) (string, error) {
	localCheckout := strings.TrimSpace(sub.CheckoutPath)
	if localCheckout == "" {
		return "", nil
	}
	remoteURL := strings.TrimSpace(sub.CheckoutRemoteURL)
	if remoteURL == "" {
		remoteURL = strings.TrimSpace(sub.URL)
	}
	if remoteURL == "" {
		return "", fmt.Errorf("subscription %s has local_checkout=%q but no checkout_remote_url", sub.ID, localCheckout)
	}
	branch := strings.TrimSpace(sub.Branch)
	if branch == "" {
		return "", fmt.Errorf("subscription %s has local_checkout=%q but no branch", sub.ID, localCheckout)
	}

	mirror, err := checkout.OpenMirror(checkout.MirrorSpec{
		Name:         "forge subscription " + sub.ID,
		WorktreePath: localCheckout,
		Logger:       s.logger,
	})
	if err != nil {
		return "", err
	}
	result, err := mirror.Sync(ctx, checkout.MirrorSyncRequest{
		RemoteURL: remoteURL,
		Branch:    branch,
	})
	if err != nil {
		return "", err
	}
	return result.RemoteHead, nil
}
