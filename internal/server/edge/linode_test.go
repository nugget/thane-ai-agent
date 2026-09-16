package edge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libdns/libdns"
)

// fakeLinode is enough of the Linode DNS API for the provider: one
// domain, a record table, and the documented error envelope.
type fakeLinode struct {
	mu      sync.Mutex
	token   string
	domain  string
	nextID  int
	records map[int]linodeRecord
	fail    map[string]int // "METHOD path" → status to return
	seen    []string
}

func newFakeLinode(t *testing.T) (*fakeLinode, *httptest.Server) {
	t.Helper()
	f := &fakeLinode{token: "good-token", domain: "example.net", nextID: 100, records: map[int]linodeRecord{}, fail: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeLinode) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errors":[{"reason":"Invalid Token"}]}`))
		return
	}
	if code, ok := f.fail[r.Method+" "+r.URL.Path]; ok {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"errors":[{"reason":"Simulated failure","field":"target"}]}`))
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v4/domains":
		var filter map[string]string
		_ = json.Unmarshal([]byte(r.Header.Get("X-Filter")), &filter)
		if filter["domain"] != f.domain {
			_, _ = w.Write([]byte(`{"data":[],"pages":1}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":7,"domain":"example.net"}],"pages":1}`))
	case r.Method == http.MethodPost && r.URL.Path == "/v4/domains/7/records":
		var rec linodeRecord
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		rec.ID = f.nextID
		f.nextID++
		f.records[rec.ID] = rec
		_ = json.NewEncoder(w).Encode(rec)
	case r.Method == http.MethodGet && r.URL.Path == "/v4/domains/7/records":
		data := make([]linodeRecord, 0, len(f.records))
		for _, rec := range f.records {
			data = append(data, rec)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "pages": 1})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v4/domains/7/records/"):
		var id int
		_, _ = fmtSscanf(strings.TrimPrefix(r.URL.Path, "/v4/domains/7/records/"), &id)
		if _, ok := f.records[id]; !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"reason":"Not found"}]}`))
			return
		}
		delete(f.records, id)
		_, _ = w.Write([]byte(`{}`))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"reason":"Not found"}]}`))
	}
}

func fmtSscanf(s string, id *int) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	*id = n
	return 1, nil
}

func testLinode(srv *httptest.Server, token string) *linodeProvider {
	p := &linodeProvider{APIToken: token, APIURL: srv.URL}
	p.ready(slog.New(slog.DiscardHandler))
	return p
}

func TestLinodeAppendAndDelete(t *testing.T) {
	t.Parallel()
	fake, srv := newFakeLinode(t)
	p := testLinode(srv, "good-token")
	ctx := context.Background()

	added, err := p.AppendRecords(ctx, "example.net.", []libdns.Record{
		libdns.TXT{Name: "_acme-challenge.thane", TTL: 30 * time.Second, Text: "token-one"},
		libdns.TXT{Name: "@", Text: "apex-token"},
	})
	if err != nil {
		t.Fatalf("AppendRecords: %v", err)
	}
	if len(added) != 2 {
		t.Fatalf("added %d records, want 2", len(added))
	}
	fake.mu.Lock()
	var apex, sub *linodeRecord
	for _, rec := range fake.records {
		rec := rec
		switch rec.Name {
		case "":
			apex = &rec
		case "_acme-challenge.thane":
			sub = &rec
		}
	}
	fake.mu.Unlock()
	if sub == nil || sub.Type != "TXT" || sub.Target != "token-one" || sub.TTLSec != 30 {
		t.Fatalf("subdomain record stored as %+v", sub)
	}
	if apex == nil || apex.Target != "apex-token" {
		t.Fatalf("apex record stored as %+v (libdns @ must map to Linode's empty name)", apex)
	}
	if got := added[1].RR().Name; got != "@" {
		t.Fatalf("apex record returned with name %q, want @", got)
	}

	deleted, err := p.DeleteRecords(ctx, "example.net.", []libdns.Record{
		libdns.TXT{Name: "_acme-challenge.thane", Text: "token-one"},
		libdns.TXT{Name: "_acme-challenge.thane", Text: "never-created"},
	})
	if err != nil {
		t.Fatalf("DeleteRecords: %v", err)
	}
	if len(deleted) != 1 {
		t.Fatalf("deleted %d records, want 1 (the unmatched input is ignored)", len(deleted))
	}
	fake.mu.Lock()
	remaining := len(fake.records)
	fake.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("%d records remain, want 1 (the apex record)", remaining)
	}
}

func TestLinodeAppendReturnsAPIErrors(t *testing.T) {
	t.Parallel()
	t.Run("bad token surfaces the API reason", func(t *testing.T) {
		t.Parallel()
		_, srv := newFakeLinode(t)
		p := testLinode(srv, "expired")
		_, err := p.AppendRecords(context.Background(), "example.net.", []libdns.Record{libdns.TXT{Name: "x", Text: "y"}})
		if err == nil || !strings.Contains(err.Error(), "Invalid Token") {
			t.Fatalf("err = %v, want the API's Invalid Token reason", err)
		}
	})
	t.Run("record creation failure is returned, not swallowed", func(t *testing.T) {
		t.Parallel()
		fake, srv := newFakeLinode(t)
		fake.fail["POST /v4/domains/7/records"] = http.StatusBadRequest
		p := testLinode(srv, "good-token")
		added, err := p.AppendRecords(context.Background(), "example.net.", []libdns.Record{libdns.TXT{Name: "x", Text: "y"}})
		if err == nil || !strings.Contains(err.Error(), "Simulated failure") {
			t.Fatalf("err = %v, want the create failure", err)
		}
		if len(added) != 0 {
			t.Fatalf("reported %d added records after a failed create", len(added))
		}
	})
	t.Run("unknown zone is an error", func(t *testing.T) {
		t.Parallel()
		_, srv := newFakeLinode(t)
		p := testLinode(srv, "good-token")
		_, err := p.AppendRecords(context.Background(), "other.net.", []libdns.Record{libdns.TXT{Name: "x", Text: "y"}})
		if err == nil || !strings.Contains(err.Error(), "not a domain on this account") {
			t.Fatalf("err = %v, want unknown-zone error", err)
		}
	})
}

func TestLinodeDoesNotTouchDefaultLogger(t *testing.T) {
	before := slog.Default()
	_, srv := newFakeLinode(t)
	p := testLinode(srv, "good-token")
	_, _ = p.AppendRecords(context.Background(), "example.net.", []libdns.Record{libdns.TXT{Name: "x", Text: "y"}})
	if slog.Default() != before {
		t.Fatal("provider replaced the process default logger")
	}
}
