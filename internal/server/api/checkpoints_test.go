package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/platform/checkpoint"
)

func TestCheckpointRestoreUnsupported(t *testing.T) {
	s, store := newConvTestServer(t)
	addConv(t, store, "live", 2, nil)
	cp, err := checkpoint.NewCheckpointer(store.DB(), testAPILogger())
	if err != nil {
		t.Fatal(err)
	}
	s.SetCheckpointer(cp)
	cp.SetProviders(func() ([]checkpoint.Conversation, error) {
		return []checkpoint.Conversation{{ID: "snapshot", Messages: []checkpoint.Message{{Role: "user", Content: "old state"}}}}, nil
	}, nil, nil)
	snapshot, err := cp.Create(checkpoint.TriggerManual, "restore regression")
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.GetAllConversations(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		id   string
		code int
	}{
		{"existing checkpoint", snapshot.ID.String(), http.StatusNotImplemented},
		{"missing checkpoint", uuid.NewString(), http.StatusNotImplemented},
		{"invalid id", "invalid", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/checkpoints/"+tc.id+"/restore", nil))
			if rr.Code != tc.code {
				t.Fatalf("status = %d, want %d; body = %s", rr.Code, tc.code, rr.Body.String())
			}
			var body struct {
				Status string `json:"status"`
				Error  struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Status != "" || body.Error.Code != tc.code || body.Error.Message == "" {
				t.Fatalf("expected an error without a success acknowledgement: %s", rr.Body.String())
			}
		})
	}
	if after, err := store.GetAllConversations(context.Background()); err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("restore changed live conversations: got %+v, error %v; want %+v", after, err, before)
	}
	stored, err := cp.Get(snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored.State, snapshot.State) {
		t.Fatalf("restore changed captured state: got %+v, want %+v", stored.State, snapshot.State)
	}
}

func TestCheckpointRestoreNotConfigured(t *testing.T) {
	s := &Server{logger: testAPILogger()}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/checkpoints/"+uuid.NewString()+"/restore", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", rr.Code, rr.Body.String())
	}
}
