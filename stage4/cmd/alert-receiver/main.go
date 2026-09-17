package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type WebhookPayload struct {
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`
	TruncatedAlerts   int               `json:"truncatedAlerts,omitempty"`
	Status            string            `json:"status"` // "firing" / "resolved"
	Receiver          string            `json:"receiver"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	Alerts            []Alert           `json:"alerts"`
}

type Alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

type receiver struct {
	logger *slog.Logger
}

func (r *receiver) handleWebhook(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload WebhookPayload
	// json parse + body size limit(prevent huge payload overload mem)
	dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<20)) // 1MiB
	if err := dec.Decode(&payload); err != nil {
		r.logger.Error("failed to decode webhook payload", slog.Any("err", err))
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// serverity
	for _, alert := range payload.Alerts {
		sev := alert.Labels["severity"]
		msg := "alert notification"
		atts := []slog.Attr{
			slog.String("status", alert.Status),
			slog.String("alertname", alert.Labels["alertname"]),
			slog.String("severity", sev),
			slog.Time("starts_at", alert.StartsAt),
			slog.String("summary", alert.Annotations["summary"]),
			slog.String("description", alert.Annotations["description"]),
			slog.String("group_key", payload.GroupKey),
			slog.String("receiver", payload.Receiver),
		}

		switch payload.Status {
		case "firing":
			switch sev {
			case "page":
				r.logger.LogAttrs(req.Context(), slog.LevelError, msg, atts...)
			default: // ticket -> warning
				r.logger.LogAttrs(req.Context(), slog.LevelWarn, msg, atts...)
			}
		case "resolved":
			r.logger.LogAttrs(req.Context(), slog.LevelInfo, "alert resolved", atts...)
		}
	}

	w.WriteHeader(http.StatusAccepted) // 202 -> AM just care U'd received
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	mux := http.NewServeMux()
	rcv := &receiver{logger: logger}
	mux.HandleFunc("/webhook", rcv.handleWebhook)
	mux.HandleFunc("/healthz", handleHealth)
	mux.HandleFunc("/readyz", handleHealth)

	addr := envOr("ADDR", ":8080")
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("alert-receiver listening", slog.String("addr", addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", slog.Any("err", err))
			os.Exit(1)
		}
	}()

	// graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	sig := <-quit
	logger.Info("shutting down", slog.String("signal", sig.String()))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("forced shutdown", slog.Any("err", err))
		os.Exit(1)
	}
	logger.Info("bye")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
