package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

	srvStat := &ServeStat{
		ObsConfig: ObsConfig{
			SkipPath: map[string]bool{
				"/metrics": true,
			},
			UnNormalPath: map[string]bool{
				"/healthz": true,
				"/readyz":  true,
			},
		},
		cfg: &Config{
			HandlerTimeout: 2 * time.Second,
			APIToken:       "token",
		},
	}
	srvStat.Ready.Store(true)
	handler := newHandler(srvStat)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Add("Authorization", "Bearer token")
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
		cfg: &Config{
			HandlerTimeout: 2 * time.Second,
			APIToken:       "token",
		},
	}
	stat.Ready.Store(false)
	handler := newHandler(stat)

	req := httptest.NewRequest("GET", "/readyz", nil)
	req.Header.Set("Authorization", "Bearer token")
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
		cfg: &Config{
			HandlerTimeout: 2 * time.Second,
			APIToken:       "token",
		},
	}
	stat.Ready.Store(true)
	handler := newHandler(stat)

	// Do /ping
	req := httptest.NewRequest("GET", "/ping", nil)
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Do /healthz /readyz
	req = httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set("Authorization", "Bearer token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	req = httptest.NewRequest("GET", "/readyz", nil)
	req.Header.Set("Authorization", "Bearer token")
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

func TestObservabilityLevel(t *testing.T) {
	obsCfg := ObsConfig{
		SkipPath: map[string]bool{
			"/metrics": true,
		},
		UnNormalPath: map[string]bool{
			"/healthz": true,
			"/readyz":  true,
		},
	}

	type obsLog struct {
		Time   string `json:"time,omitempty"`
		Level  string `json:"level,omitempty"`
		Msg    string `json:"msg,omitempty"`
		Path   string `json:"path,omitempty"`
		Status int    `json:"status,omitempty"`
		Err    string `json:"err,omitempty"`
	}

	// table-driven：status -> 期望 level
	tests := []struct {
		name    string
		status  int
		wantLvl string
		wantMsg string
	}{
		{name: "500 internal server error", status: http.StatusInternalServerError, wantLvl: "ERROR", wantMsg: "request"},
		{name: "401 unauthorized", status: http.StatusUnauthorized, wantLvl: "WARN", wantMsg: "request"},
		{name: "200 ok", status: http.StatusOK, wantLvl: "INFO", wantMsg: "request"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 每个 case 独立 buffer，避免 Reset 忘写导致串台
			buf := &bytes.Buffer{}
			oldLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
			t.Cleanup(func() { slog.SetDefault(oldLogger) })

			hf := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
			})
			handler := withObservaility(obsCfg)(hf)

			req := httptest.NewRequest(http.MethodGet, "/ping", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			// ---- 关键防御：按行拆，只取 msg=="request" 那一行再 Unmarshal ----
			var (
				selected []byte
				found    bool
			)
			for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
				line = strings.TrimSpace(line) // 顺手兜一下 \r\n / 空行
				if line == "" {
					continue
				}
				// 先只 peek 一下 msg 字段，不整包解（轻量筛选）
				var peek struct {
					Msg string `json:"msg"`
				}
				if err := json.Unmarshal([]byte(line), &peek); err != nil {
					// 这一行不是 JSON（比如别的中间件打的纯文本），跳过而不是炸测试
					continue
				}
				if peek.Msg == tt.wantMsg {
					selected = []byte(line)
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("no %q log line found in output:\n%s", tt.wantMsg, buf.String())
			}

			got := &obsLog{}
			if err := json.Unmarshal(selected, got); err != nil {
				t.Fatalf("unmarshal request-log line failed: %v\nline=%s", err, string(selected))
			}

			if got.Level != tt.wantLvl {
				t.Errorf("logLevel = %q, want %q (status=%d)", got.Level, tt.wantLvl, tt.status)
			}
			if got.Status != tt.status {
				t.Errorf("logStatus = %d, want %d", got.Status, tt.status)
			}
			if got.Path != "/ping" {
				t.Errorf("logPath = %q, want /ping", got.Path)
			}
		})
	}
}

func TestPanicVisibleToObserability(t *testing.T) {
	type obsLog struct {
		Time   string `json:"time,omitempty"`
		Level  string `json:"level,omitempty"`
		Msg    string `json:"msg,omitempty"`
		Path   string `json:"path,omitempty"`
		Status int    `json:"status,omitempty"`
		Err    string `json:"err,omitempty"`
	}

	httpRequestsTotal.Reset()
	buf := &bytes.Buffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(old) })

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
		cfg: &Config{
			HandlerTimeout: 2 * time.Second,
			APIToken:       "token",
		},
	}
	stat.Ready.Store(true)

	boom := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})
	handler := chain(stat, boom)

	// request with token
	req := httptest.NewRequest("GET", "/ping", nil)
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	// assert: client takes 500
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("stats = %d, want 500", rec.Code)
	}

	// assert: exsit request line logging out with level ERROR and status eq 500
	var (
		selected []byte
		found    bool
	)
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		line = strings.TrimSpace(line) // 顺手兜一下 \r\n / 空行
		if line == "" {
			continue
		}
		// 先只 peek 一下 msg 字段，不整包解（轻量筛选）
		var peek struct {
			Msg string `json:"msg"`
		}
		if err := json.Unmarshal([]byte(line), &peek); err != nil {
			// 这一行不是 JSON（比如别的中间件打的纯文本），跳过而不是炸测试
			continue
		}
		if peek.Msg == "request" {
			selected = []byte(line)
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no %q log line found in output:\n%s", "request", buf.String())
	}

	got := &obsLog{}
	if err := json.Unmarshal(selected, got); err != nil {
		t.Fatalf("unmarshal request-log line failed: %v\nline=%s", err, string(selected))
	}

	if got.Level != "ERROR" {
		t.Errorf("logLevel = %q, want %q (status=%d)", got.Level, "ERROR", 500)
	}
	if got.Status != 500 {
		t.Errorf("logStatus = %d, want %d", got.Status, 500)
	}
	if got.Path != "/ping" {
		t.Errorf("logPath = %q, want /ping", got.Path)
	}

	promGot := testutil.ToFloat64(httpRequestsTotal.WithLabelValues("GET", "/ping", "500"))
	if promGot != 1 {
		t.Errorf("promGot = %f, want 1", promGot)
	}
}
