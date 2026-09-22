package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Config
type Config struct {
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ListenAddr        string

	DrainTimeout time.Duration
	LogLevel     slog.Level
	APIToken     string

	// For mux write timeout, but is it dupilate with WriteTimeout?
	HandlerTimeout time.Duration

	// Set a fail rage for pingHandler
	FAILRATE int
}

// Set golbal variable as failRate
var failRate int

func (c *Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("listen_addr", c.ListenAddr),
		slog.String("read_header_timeout", c.ReadHeaderTimeout.String()),
		slog.String("read_timeout", c.ReadTimeout.String()),
		slog.String("write_timeout", c.WriteTimeout.String()),
		slog.String("idle_timeout", c.IdleTimeout.String()),
		slog.String("drain_timeout", c.DrainTimeout.String()),
		slog.Any("log_level", c.LogLevel),
		slog.String("handler_timeout", c.HandlerTimeout.String()),
		slog.Int("fail_rate", c.FAILRATE),
	)
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// slog midware laying 1.always not log 2.only log expel 200 3.always log
type ObsConfig struct {
	// always skip
	SkipPath map[string]bool
	// skip 200, slog other
	UnNormalPath map[string]bool
}

// ServeStat
type ServeStat struct {
	Ready atomic.Bool
	ObsConfig
	cfg *Config
}

// wrap existing htt.ResponseWriter with httpCode
type statusRecoder struct {
	http.ResponseWriter
	status int
}

// metrics
var (
	httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"method", "path", "status"})
	httpRequestDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency",
			Buckets: []float64{.00001, .00002, .00025, .0005, .001, .0025, .005, .01},
		},
		[]string{"method", "path"})
)

func (rec *statusRecoder) WriteHeader(code int) {
	rec.status = code
	rec.ResponseWriter.WriteHeader(code)
}

func init() {
	prometheus.MustRegister(
		httpRequestsTotal,
		httpRequestDurationSeconds,
	)
}

func loadConfig() (*Config, error) {
	var errs []error

	dur := func(key string, def time.Duration) time.Duration {
		v := os.Getenv(key)
		if v == "" {
			return def
		}

		d, err := time.ParseDuration(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s:%q: %w", key, v, err))
			return def
		}

		return d
	}

	logLevel := func(key string, def slog.Level) slog.Level {
		v := os.Getenv(key)
		if v == "" {
			return def
		}

		var lvl slog.Level
		switch strings.ToLower(v) {
		case "debug":
			lvl = slog.LevelDebug
		case "info":
			lvl = slog.LevelInfo
		case "warn", "warning":
			lvl = slog.LevelWarn
		case "error":
			lvl = slog.LevelError
		default:
			errs = append(errs, fmt.Errorf("%s:%q: %w", key, v, errors.New("invalid loglevel format")))
		}
		return lvl
	}

	getToken := func(key string) string {
		token := os.Getenv(key)
		if token == "" {
			errs = append(errs, fmt.Errorf("%s:%q: %w", key, token, errors.New("token acquire")))
		}

		return token
	}

	// FAILE_RATE from CM(string) need convert to struct.FAILRATE(int)
	failRate, _ = strconv.Atoi(getEnv("FAIL_RATE", "0"))

	cfg := &Config{
		ListenAddr:        getEnv("LISTEN_ADDR", ":8080"),
		ReadHeaderTimeout: dur("READ_HEADER_TIMEOUT", 5*time.Second),
		ReadTimeout:       dur("READ_TIMEOUT", 10*time.Second),
		WriteTimeout:      dur("WRITE_TIMEOUT", 10*time.Second),
		IdleTimeout:       dur("IDLE_TIMEOUT", 60*time.Second),
		DrainTimeout:      dur("DRAIN_TIMEOUT", 3*time.Second),
		LogLevel:          logLevel("LOG_LEVEL", slog.LevelInfo),
		APIToken:          getToken("API_TOKEN"),
		HandlerTimeout:    dur("HANDLER_TIMEOUT", 2*time.Second),
		FAILRATE:          failRate,
	}
	// Always print current readed env whatever loglevel be seted
	// Here i just prehandle it order before levelVar.Set()
	// So it will be printed all the time at the preface of application
	slog.Info("config loaded", slog.Any("config", cfg))
	return cfg, errors.Join(errs...)
}

func main() {

	var levelVar slog.LevelVar
	// create json log logger and set it as Defaultlogger
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: &levelVar}))
	slog.SetDefault(logger)
	// Set MemoryLimit eq resources.limit 90%
	debug.SetMemoryLimit(debug.SetMemoryLimit(-1) * 9 / 10)
	slog.Info("runtime visibility",
		"num_cpu", runtime.NumCPU(),
		"gomaxprocs", runtime.GOMAXPROCS(0),
		"gomemlimit_bytes", debug.SetMemoryLimit(-1),
	)
	// load config from Env
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("invalid config", "err", err)
		os.Exit(1)
	}

	// set LogLevel
	levelVar.Set(cfg.LogLevel)
	// set failRate
	failRate = cfg.FAILRATE

	// healthy check
	// /metrics, it should never logging for it was provided metrics prolong
	// or should never be count in prometheus because it is count metrics alreay
	// mutitimes count and count encharge the request base number.
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
		cfg: cfg,
	}
	srvStat.Ready.Store(true)

	slog.Info("server startng listen")
	srv := &http.Server{
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		Addr:              cfg.ListenAddr,
		Handler:           newHandler(srvStat),
	}
	go func(srv *http.Server) {
		err := srv.ListenAndServe()
		if err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				slog.Info("server is shutting down, waitting for handling the rest request...")
				return
			}
			slog.Error("server failed", "err", err)
			os.Exit(1)
		}
	}(srv)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	slog.Info("server receive signals, preparing to shut down...")
	// receive signals, readyz should return 503
	srvStat.Ready.Store(false)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	if err != nil {
		slog.Error("shutdown failed", "err", err)
	}
	slog.Info("server is shutdowned gracefully")
	// normally graceful shutdown, return 0 default
	// no more need redundant os.Exit(0) or return
}

func chain(stat *ServeStat, inner http.Handler) http.Handler {
	return withObservaility(stat.ObsConfig)(
		withRecovery(
			withAuthenticator(stat.cfg)(
				inner)))
}
func newHandler(stat *ServeStat) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", pingHandler)
	mux.HandleFunc("GET /healthz", stat.healthzHandler)
	mux.HandleFunc("GET /readyz", stat.readyzHandler)
	mux.Handle("GET /metrics", promhttp.Handler())
	return chain(stat, http.TimeoutHandler(mux, stat.cfg.HandlerTimeout, "request timeout\n"))
}

func pingHandler(w http.ResponseWriter, r *http.Request) {
	if failRate > 0 && rand.IntN(100) < failRate {
		http.Error(w, "server internal error", http.StatusInternalServerError)
		return
	}
	_, err := w.Write([]byte("pong\n"))
	if err != nil {
		// current Write Error should not Exit Server
		// so it won't have os.Exit(1)
		slog.Error("response failed", "err", err)
	}
}

func (s *ServeStat) healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *ServeStat) readyzHandler(w http.ResponseWriter, r *http.Request) {
	if s.Ready.Load() {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
}

func withAuthenticator(cfg *Config) func(http.Handler) http.Handler {
	expectedToken := cfg.APIToken
	if expectedToken == "" {
		// actually, this below is never print, cause when cfg.ApiToken is empty
		// it has reject and cannot start
		slog.Warn("API_TOKEN not set, reject all requests")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// health check request allow
			if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics" {
				next.ServeHTTP(w, r)
				return
			}

			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				slog.Warn("Authorization header require", "path", r.URL.Path)
				http.Error(w, "Authorization header require", http.StatusUnauthorized)
				return
			}

			// format has tobe "Bearer <token>"
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
				slog.Warn("invalid Authorization format", "authorization", authHeader)
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}

			token := parts[1]
			if subtle.ConstantTimeCompare([]byte(token), []byte(expectedToken)) != 1 {
				http.Error(w, "invalide token", http.StatusUnauthorized)
				return
			}

			slog.Debug("auth passed", "path", r.URL.Path)
			next.ServeHTTP(w, r)
		})
	}
}
func withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic occur",
					"err", r,
				)
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func withObservaility(obsconfig ObsConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// call itself handler
			// status:200 behalf defalt 200 implicitly before handler didn't call WriteHeader
			rec := &statusRecoder{ResponseWriter: w, status: 200}
			next.ServeHTTP(rec, r)

			// if request path was SkipePath, return it, donot slog anything
			if obsconfig.SkipPath[r.URL.Path] {
				return
			}

			// if request path was UnNormalPath, i'll just log excluding 200
			// situation with WARN level, otherwise pass
			if obsconfig.UnNormalPath[r.URL.Path] {
				if rec.status == 200 {
					return
				}
			}

			level := levelForStatus(rec.status)

			// After request, printing log
			slog.Log(
				context.Background(),
				level,
				"request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_us", time.Since(start).Microseconds())

			// export prometheus metrics
			httpRequestsTotal.With(prometheus.Labels{
				"method": r.Method,
				"path":   r.URL.Path,
				"status": strconv.Itoa(rec.status),
			}).Inc()

			httpRequestDurationSeconds.With(prometheus.Labels{
				"method": r.Method,
				"path":   r.URL.Path,
			}).Observe(time.Since(start).Seconds())
		})
	}
}

func levelForStatus(status int) slog.Level {
	// Set LogLevel for diff status
	switch {
	case status >= 500:
		return slog.LevelError
	case status >= 400:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}
