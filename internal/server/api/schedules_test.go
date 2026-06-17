package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/scheduler"
)

// fakeScheduler is a canned SchedulerReader for handler tests. GetTask returns
// sql.ErrNoRows for unknown ids, matching the store's real not-found behavior.
type fakeScheduler struct {
	tasks   []*scheduler.Task
	byID    map[string]*scheduler.Task
	execs   map[string][]*scheduler.Execution
	listErr error
}

func (f fakeScheduler) ListTasks(enabledOnly bool) ([]*scheduler.Task, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if !enabledOnly {
		return f.tasks, nil
	}
	var out []*scheduler.Task
	for _, t := range f.tasks {
		if t.Enabled {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f fakeScheduler) GetTask(id string) (*scheduler.Task, error) {
	if t, ok := f.byID[id]; ok {
		return t, nil
	}
	return nil, sql.ErrNoRows
}

func (f fakeScheduler) GetTaskExecutions(taskID string, _ int) ([]*scheduler.Execution, error) {
	return f.execs[taskID], nil
}

func schedServer(r SchedulerReader) *Server {
	return &Server{
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		schedulerReader: r,
	}
}

// everyHour is an enabled recurring task whose NextRun always resolves to a
// future fire time, so its view carries next_run.
func everyHour() *scheduler.Task {
	return &scheduler.Task{
		ID:        "t1",
		Name:      "heartbeat",
		Enabled:   true,
		Schedule:  scheduler.Schedule{Kind: scheduler.ScheduleEvery, Every: &scheduler.Duration{Duration: time.Hour}},
		Payload:   scheduler.Payload{Kind: scheduler.PayloadWake},
		CreatedAt: at(1000),
	}
}

// scheduleResp mirrors the scheduleView wire shape: promoted Task fields plus
// the derived next_run.
type scheduleResp struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	NextRun *time.Time `json:"next_run"`
}

func TestHandleSchedules(t *testing.T) {
	fake := fakeScheduler{tasks: []*scheduler.Task{
		everyHour(),
		{ID: "t2", Name: "oneshot-past", Enabled: false, CreatedAt: at(2000),
			Schedule: scheduler.Schedule{Kind: scheduler.ScheduleAt, At: ptrTime(at(500))}},
	}}
	s := schedServer(fake)

	rr := httptest.NewRecorder()
	s.handleSchedules(rr, httptest.NewRequest(http.MethodGet, "/v1/schedules", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if b := strings.TrimSpace(rr.Body.String()); !strings.HasPrefix(b, "[") {
		t.Fatalf("body = %s, want a bare JSON array", b)
	}
	var all []scheduleResp
	if err := json.Unmarshal(rr.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("schedules = %d, want 2", len(all))
	}
	if all[0].ID != "t1" || all[0].Name != "heartbeat" {
		t.Errorf("first = %+v, want promoted id/name from the embedded task", all[0])
	}
	if all[0].NextRun == nil || !all[0].NextRun.After(time.Now()) {
		t.Errorf("recurring task next_run = %v, want a future time", all[0].NextRun)
	}
	if all[1].NextRun != nil {
		t.Errorf("past one-shot next_run = %v, want omitted", all[1].NextRun)
	}

	// ?enabled=true narrows to enabled tasks.
	rr2 := httptest.NewRecorder()
	s.handleSchedules(rr2, httptest.NewRequest(http.MethodGet, "/v1/schedules?enabled=true", nil))
	var enabled []scheduleResp
	if err := json.Unmarshal(rr2.Body.Bytes(), &enabled); err != nil {
		t.Fatalf("decode enabled: %v", err)
	}
	if len(enabled) != 1 || enabled[0].ID != "t1" {
		t.Errorf("?enabled=true = %+v, want exactly [t1]", enabled)
	}
}

func TestHandleSchedules_Unconfigured(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rr := httptest.NewRecorder()
	s.handleSchedules(rr, httptest.NewRequest(http.MethodGet, "/v1/schedules", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when scheduler unset", rr.Code)
	}
}

func TestHandleSchedules_QueryError(t *testing.T) {
	s := schedServer(fakeScheduler{listErr: errors.New("boom")})
	rr := httptest.NewRecorder()
	s.handleSchedules(rr, httptest.NewRequest(http.MethodGet, "/v1/schedules", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 when ListTasks errors", rr.Code)
	}
}

func TestHandleSchedule_Found(t *testing.T) {
	s := schedServer(fakeScheduler{byID: map[string]*scheduler.Task{"t1": everyHour()}})
	req := httptest.NewRequest(http.MethodGet, "/v1/schedules/t1", nil)
	req.SetPathValue("id", "t1")
	rr := httptest.NewRecorder()
	s.handleSchedule(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got scheduleResp
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "t1" || got.NextRun == nil {
		t.Errorf("got = %+v, want t1 with a next_run", got)
	}
}

func TestHandleSchedule_NotFound(t *testing.T) {
	s := schedServer(fakeScheduler{byID: map[string]*scheduler.Task{}})
	req := httptest.NewRequest(http.MethodGet, "/v1/schedules/nope", nil)
	req.SetPathValue("id", "nope")
	rr := httptest.NewRecorder()
	s.handleSchedule(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for unknown id", rr.Code)
	}
}

func TestHandleScheduleExecutions_BareArrayNewestFirst(t *testing.T) {
	s := schedServer(fakeScheduler{
		byID: map[string]*scheduler.Task{"t1": everyHour()},
		execs: map[string][]*scheduler.Execution{
			"t1": {
				{ID: "e2", TaskID: "t1", ScheduledAt: at(300), Status: scheduler.StatusCompleted},
				{ID: "e1", TaskID: "t1", ScheduledAt: at(200), Status: scheduler.StatusFailed},
			},
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/schedules/t1/executions", nil)
	req.SetPathValue("id", "t1")
	rr := httptest.NewRecorder()
	s.handleScheduleExecutions(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var execs []scheduler.Execution
	if err := json.Unmarshal(rr.Body.Bytes(), &execs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(execs) != 2 || execs[0].ID != "e2" {
		t.Errorf("executions = %+v, want the store's newest-first order [e2,e1]", execs)
	}
}

func TestHandleScheduleExecutions_EmptyIsArrayNotNull(t *testing.T) {
	s := schedServer(fakeScheduler{byID: map[string]*scheduler.Task{"t3": {ID: "t3", Name: "quiet"}}})
	req := httptest.NewRequest(http.MethodGet, "/v1/schedules/t3/executions", nil)
	req.SetPathValue("id", "t3")
	rr := httptest.NewRecorder()
	s.handleScheduleExecutions(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if b := strings.TrimSpace(rr.Body.String()); b != "[]" {
		t.Errorf("empty body = %q, want %q", b, "[]")
	}
}

func TestHandleScheduleExecutions_UnknownTaskIs404(t *testing.T) {
	s := schedServer(fakeScheduler{byID: map[string]*scheduler.Task{}})
	req := httptest.NewRequest(http.MethodGet, "/v1/schedules/ghost/executions", nil)
	req.SetPathValue("id", "ghost")
	rr := httptest.NewRecorder()
	s.handleScheduleExecutions(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for unknown task", rr.Code)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
