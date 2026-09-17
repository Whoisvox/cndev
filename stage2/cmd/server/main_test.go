package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestResponse(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "GetPing200",
			method:     "GET",
			path:       "/ping",
			wantStatus: 200,
			wantBody:   "pong\n",
		},
		{
			name:       "PostPing405",
			method:     "POST",
			path:       "/ping",
			wantStatus: 405,
			wantBody:   "Method Not Allowed\n",
		},
		{
			name:       "NonExsitPage404",
			method:     "GET",
			path:       "/idontexsit",
			wantStatus: 404,
			wantBody:   "404 page not found\n",
		},
	}

	srvStat := &ServeStat{}
	srvStat.Ready.Store(true)
	handler := newHandler(srvStat)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if rec.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tt.wantBody)
			}

		})
	}
}

func TestReadyzUnready(t *testing.T) {
	stat := &ServeStat{}
	stat.Ready.Store(false)
	handler := newHandler(stat)

	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}

}

func TestRecovery(t *testing.T) {
	panicky := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})

	handler := withRecovery(panicky)

	req := httptest.NewRequest(http.MethodGet, "/iwillmakeboom", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestProbeNotCounted(t *testing.T) {
	// global metrics reset to zero excluding smudge from other TestFunc at this current file.
	httpRequestsTotal.Reset()

	stat := &ServeStat{
		ObsConfig: ObsConfig{
			SkipPath: map[string]bool{
				"/metrics": true,
			},
			UnNormalPath: map[string]bool{
				"/healthz": true,
				"/readyz":  true,
			},
		},
	}
	stat.Ready.Store(true)
	handler := newHandler(stat)

	// Do /ping
	req := httptest.NewRequest("GET", "/ping", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Do /healthz /readyz
	req = httptest.NewRequest("GET", "/healthz", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	req = httptest.NewRequest("GET", "/readyz", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Do /metrics
	req = httptest.NewRequest("GET", "/metrics", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	got := testutil.ToFloat64(httpRequestsTotal.WithLabelValues("GET", "/ping", "200"))
	if got != 1 {
		t.Errorf("got: %v, wanna %d\n", got, 1)
	}

	count := testutil.CollectAndCount(httpRequestsTotal)
	if count != 1 {
		t.Errorf("counter: %d, wanna %d\n", count, 1)
	}

}

func TestLogLevel(t *testing.T) {
	httpRequestsTotal.Reset()

	stat := &ServeStat{
		cfg: &Config{
			FAILRATE: 100,
		},
		ObsConfig: ObsConfig{
			SkipPath: map[string]bool{
				"/metrics": true,
			},
			UnNormalPath: map[string]bool{
				"/healthz": true,
				"/readyz":  true,
			},
		},
	}

	stat.Ready.Store(true)
	handler := newHandler(stat)

	// Do /ping without authriozation
	req := httptest.NewRequest("GET", "/ping", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
}
