package contacts

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

func TestPresenceTrackerObserveRoomConsensus(t *testing.T) {
	tracker := NewPresenceTracker([]string{"person.alice"}, "UTC", nil)
	phoneAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	watchAt := phoneAt.Add(time.Second)

	tracker.ObserveRoom("person.alice", RoomObservation{
		Room:       "Office",
		Provider:   " Bermuda ",
		Source:     "device_tracker.phone_bermuda",
		Via:        "Desk Presence",
		ObservedAt: phoneAt,
	})
	first, _ := tracker.Snapshot("person.alice")
	if first.Room != "Office" || first.RoomProvider != "bermuda" || first.RoomSource != "Desk Presence" {
		t.Fatalf("single-source resolution = %+v", first)
	}

	tracker.ObserveRoom("person.alice", RoomObservation{
		Room:       " office ",
		Provider:   "bermuda",
		Source:     "device_tracker.watch_bermuda",
		Via:        "Desk Presence",
		ObservedAt: watchAt,
	})
	providerConsensus, _ := tracker.Snapshot("person.alice")
	if providerConsensus.Room != "Office" || providerConsensus.RoomProvider != "bermuda" || providerConsensus.RoomSource != "Desk Presence" {
		t.Fatalf("same-provider consensus = %+v", providerConsensus)
	}
	if providerConsensus.RoomConflict {
		t.Fatal("agreeing sources reported a conflict")
	}
	if !providerConsensus.RoomSince.Equal(first.RoomSince) {
		t.Errorf("agreeing source reset RoomSince: %v then %v", first.RoomSince, providerConsensus.RoomSince)
	}

	tracker.ObserveRoom("person.alice", RoomObservation{
		Room:       "OFFICE",
		Provider:   "unifi",
		Source:     "ap-office",
		ObservedAt: watchAt.Add(time.Second),
	})
	crossProviderConsensus, _ := tracker.Snapshot("person.alice")
	if crossProviderConsensus.Room != "Office" || crossProviderConsensus.RoomProvider != "" || crossProviderConsensus.RoomSource != "" {
		t.Fatalf("cross-provider consensus = %+v", crossProviderConsensus)
	}
	if crossProviderConsensus.RoomConflict {
		t.Fatal("case-insensitive cross-provider agreement reported a conflict")
	}
	if len(crossProviderConsensus.RoomObservations) != 3 {
		t.Fatalf("observations = %+v, want 3", crossProviderConsensus.RoomObservations)
	}
	if crossProviderConsensus.RoomObservations[0].Source != "device_tracker.phone_bermuda" ||
		crossProviderConsensus.RoomObservations[1].Source != "device_tracker.watch_bermuda" ||
		crossProviderConsensus.RoomObservations[2].Source != "ap-office" {
		t.Errorf("observations not deterministically ordered: %+v", crossProviderConsensus.RoomObservations)
	}
}

func TestPresenceTrackerRoomConflictAndRecovery(t *testing.T) {
	tracker := NewPresenceTracker([]string{"person.alice"}, "UTC", nil)
	tracker.HandleStateChange("person.alice", "", "home", "")
	tracker.ObserveRoom("person.alice", RoomObservation{
		Room: "office", Provider: "bermuda", Source: "device_tracker.phone_bermuda",
	})
	tracker.ObserveRoom("person.alice", RoomObservation{
		Room: "kitchen", Provider: "unifi", Source: "ap-kitchen",
	})

	conflicted, _ := tracker.Snapshot("person.alice")
	if !conflicted.RoomConflict {
		t.Fatal("disagreeing observations did not report a conflict")
	}
	if conflicted.Room != "" || conflicted.RoomProvider != "" || conflicted.RoomSource != "" || !conflicted.RoomSince.IsZero() {
		t.Errorf("conflict asserted a resolved room: %+v", conflicted)
	}
	if len(conflicted.RoomObservations) != 2 {
		t.Fatalf("conflict discarded evidence: %+v", conflicted.RoomObservations)
	}
	rendered, err := tracker.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, `"room_conflict":true`) || strings.Contains(rendered, `"room":`) {
		t.Fatalf("conflict context = %s, want conflict without resolved room", rendered)
	}

	tracker.WithdrawRoom("person.alice", "unifi", "ap-kitchen")
	recovered, _ := tracker.Snapshot("person.alice")
	if recovered.RoomConflict || recovered.Room != "office" || recovered.RoomProvider != "bermuda" || recovered.RoomSource != "" {
		t.Fatalf("resolution after withdrawal = %+v", recovered)
	}
	if recovered.RoomSince.IsZero() {
		t.Fatal("recovered resolution did not establish RoomSince")
	}
}

func TestPresenceTrackerWithdrawRoomPreservesOtherEvidence(t *testing.T) {
	tracker := NewPresenceTracker([]string{"person.alice"}, "UTC", nil)
	tracker.ObserveRoom("person.alice", RoomObservation{
		Room: "office", Provider: "bermuda", Source: "device_tracker.phone_bermuda",
	})
	tracker.ObserveRoom("person.alice", RoomObservation{
		Room: "office", Provider: "bermuda", Source: "device_tracker.watch_bermuda",
	})
	before, _ := tracker.Snapshot("person.alice")

	tracker.WithdrawRoom("person.alice", " BERMUDA ", "device_tracker.watch_bermuda")
	after, _ := tracker.Snapshot("person.alice")
	if after.Room != "office" || after.RoomProvider != "bermuda" || after.RoomSource != "" {
		t.Fatalf("remaining observation did not resolve: %+v", after)
	}
	if len(after.RoomObservations) != 1 {
		t.Fatalf("withdrawal removed unrelated evidence: %+v", after.RoomObservations)
	}
	if !after.RoomSince.Equal(before.RoomSince) {
		t.Errorf("same resolved room reset RoomSince: %v then %v", before.RoomSince, after.RoomSince)
	}
}

func TestPresenceTrackerObservationRefresh(t *testing.T) {
	tracker := NewPresenceTracker([]string{"person.alice"}, "UTC", nil)
	var notifications int
	tracker.OnRoomChange(func(_, _, _, _ string) { notifications++ })
	firstAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	secondAt := firstAt.Add(10 * time.Second)
	observation := RoomObservation{
		Room: "office", Provider: "bermuda", Source: "device_tracker.phone_bermuda", ObservedAt: firstAt,
	}
	tracker.ObserveRoom("person.alice", observation)
	first, _ := tracker.Snapshot("person.alice")

	observation.Room = " OFFICE "
	observation.ObservedAt = secondAt
	tracker.ObserveRoom("person.alice", observation)
	second, _ := tracker.Snapshot("person.alice")
	if notifications != 1 {
		t.Errorf("semantic refresh emitted %d notifications, want 1", notifications)
	}
	if !second.RoomObservations[0].ObservedAt.Equal(secondAt) {
		t.Errorf("ObservedAt = %v, want %v", second.RoomObservations[0].ObservedAt, secondAt)
	}
	if !second.RoomSince.Equal(first.RoomSince) {
		t.Errorf("semantic refresh reset RoomSince: %v then %v", first.RoomSince, second.RoomSince)
	}

	observation.Via = "Desk Presence"
	observation.ObservedAt = secondAt.Add(time.Second)
	tracker.ObserveRoom("person.alice", observation)
	withEvidence, _ := tracker.Snapshot("person.alice")
	if notifications != 2 {
		t.Errorf("evidence change emitted %d notifications, want 2 total", notifications)
	}
	if withEvidence.RoomSource != "Desk Presence" || !withEvidence.RoomSince.Equal(first.RoomSince) {
		t.Errorf("evidence refresh = %+v", withEvidence)
	}
}

func TestRoomObservationEvidenceKeepsBermudaIdentityPrivate(t *testing.T) {
	tests := []struct {
		name        string
		observation RoomObservation
		want        string
	}{
		{
			name:        "Bermuda without scanner",
			observation: RoomObservation{Provider: BermudaRoomProvider, Source: "device_tracker.private_watch"},
		},
		{
			name:        "Bermuda scanner",
			observation: RoomObservation{Provider: BermudaRoomProvider, Source: "device_tracker.private_watch", Via: "Desk Presence"},
			want:        "Desk Presence",
		},
		{
			name:        "UniFi AP compatibility fallback",
			observation: RoomObservation{Provider: "unifi", Source: "ap-office"},
			want:        "ap-office",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := roomObservationEvidence(tt.observation); got != tt.want {
				t.Errorf("evidence = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatPersonPresenceConflictSuppressesResolvedRoom(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	rendered := FormatPersonPresence(
		"person.alice", "Alice", "home", now.Add(-time.Hour),
		"office", "unifi", "ap-office", true, now,
	)
	if !strings.Contains(rendered, `"room_conflict":true`) || strings.Contains(rendered, `"room":`) {
		t.Fatalf("conflict context = %s, want conflict without resolved room", rendered)
	}
}

func TestPresenceTrackerUpdateRoomReplacesProviderObservation(t *testing.T) {
	tracker := NewPresenceTracker([]string{"person.alice"}, "UTC", nil)
	tracker.UpdateRoom("person.alice", "office", "unifi", "ap-office-east")
	first, _ := tracker.Snapshot("person.alice")

	tracker.UpdateRoom("person.alice", "office", "unifi", "ap-office-west")
	second, _ := tracker.Snapshot("person.alice")
	if len(second.RoomObservations) != 1 || second.RoomObservations[0].Source != "ap-office-west" {
		t.Fatalf("compatibility update retained stale provider evidence: %+v", second.RoomObservations)
	}
	if !second.RoomSince.Equal(first.RoomSince) {
		t.Errorf("same-room provider replacement reset RoomSince: %v then %v", first.RoomSince, second.RoomSince)
	}
}

func TestPresenceTrackerRoomObserverWithdrawalRetainsIdentity(t *testing.T) {
	tracker := NewPresenceTracker([]string{"person.alice"}, "UTC", nil)
	type notification struct {
		room     string
		provider string
		source   string
	}
	var notifications []notification
	tracker.OnRoomChange(func(_ string, room string, provider string, source string) {
		notifications = append(notifications, notification{room: room, provider: provider, source: source})
	})

	tracker.ObserveRoom("person.alice", RoomObservation{
		Room: "office", Provider: "unifi", Source: "ap-office",
	})
	tracker.WithdrawRoom("person.alice", "unifi", "ap-office")

	if len(notifications) != 2 {
		t.Fatalf("notifications = %+v, want observe + withdraw", notifications)
	}
	withdrawal := notifications[1]
	if withdrawal.room != "" || withdrawal.provider != "unifi" || withdrawal.source != "ap-office" {
		t.Errorf("withdrawal lost identity: %+v", withdrawal)
	}
}

func TestPresenceTrackerLegacyClearWithdrawsEveryObservation(t *testing.T) {
	tracker := NewPresenceTracker([]string{"person.alice"}, "UTC", nil)
	tracker.ObserveRoom("person.alice", RoomObservation{
		Room: "office", Provider: "bermuda", Source: "device_tracker.phone_bermuda",
	})
	tracker.ObserveRoom("person.alice", RoomObservation{
		Room: "office", Provider: "unifi", Source: "ap-office",
	})
	var withdrawn []string
	tracker.OnRoomChange(func(_ string, room string, provider string, source string) {
		if room == "" {
			withdrawn = append(withdrawn, provider+":"+source)
		}
	})

	tracker.UpdateRoom("person.alice", "", "", "")
	snapshot, _ := tracker.Snapshot("person.alice")
	if snapshot.Room != "" || snapshot.RoomConflict || len(snapshot.RoomObservations) != 0 {
		t.Errorf("legacy clear retained room state: %+v", snapshot)
	}
	if len(withdrawn) != 2 || withdrawn[0] != "bermuda:device_tracker.phone_bermuda" || withdrawn[1] != "unifi:ap-office" {
		t.Errorf("withdrawals = %v", withdrawn)
	}
}

func TestPresenceTrackerNotHomeWithdrawsEveryObservation(t *testing.T) {
	tracker := NewPresenceTracker([]string{"person.alice"}, "UTC", nil)
	tracker.HandleStateChange("person.alice", "", "home", "")
	tracker.ObserveRoom("person.alice", RoomObservation{
		Room: "office", Provider: "bermuda", Source: "device_tracker.phone_bermuda",
	})
	tracker.ObserveRoom("person.alice", RoomObservation{
		Room: "office", Provider: "unifi", Source: "ap-office",
	})
	var withdrawn []string
	tracker.OnRoomChange(func(_ string, room string, provider string, source string) {
		if room == "" {
			withdrawn = append(withdrawn, provider+":"+source)
		}
	})

	tracker.HandleStateChange("person.alice", "home", "not_home", "")
	snap, _ := tracker.Snapshot("person.alice")
	if snap.Room != "" || snap.RoomConflict || len(snap.RoomObservations) != 0 {
		t.Errorf("not_home retained room state: %+v", snap)
	}
	if len(withdrawn) != 2 || withdrawn[0] != "bermuda:device_tracker.phone_bermuda" || withdrawn[1] != "unifi:ap-office" {
		t.Errorf("withdrawals = %v", withdrawn)
	}
	rendered, _ := tracker.TagContext(context.Background(), agentctx.ContextRequest{})
	if rendered == "" {
		t.Fatal("tracked person context unexpectedly empty")
	}
}

func TestPresenceTrackerSnapshotCopiesObservations(t *testing.T) {
	tracker := NewPresenceTracker([]string{"person.alice"}, "UTC", nil)
	tracker.ObserveRoom("person.alice", RoomObservation{
		Room: "office", Provider: "unifi", Source: "ap-office",
	})

	first, _ := tracker.Snapshot("person.alice")
	first.RoomObservations[0].Room = "tampered"
	second, _ := tracker.Snapshot("person.alice")
	if second.RoomObservations[0].Room != "office" {
		t.Errorf("snapshot mutation leaked into tracker: %+v", second.RoomObservations)
	}
}
