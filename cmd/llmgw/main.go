package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pw/llmgw/internal/auth"
	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/keypool"
	"github.com/pw/llmgw/internal/router"
	"github.com/pw/llmgw/internal/server"
	"github.com/pw/llmgw/internal/store"
	"github.com/pw/llmgw/internal/transport"
)

func main() {
	var (
		configPath = flag.String("config", "config.yaml", "path to config file")
		addr       = flag.String("addr", "", "override server.addr")
	)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}
	if *addr != "" {
		cfg.Server.Addr = *addr
	}

	logger := newLogger(cfg.Log)
	slog.SetDefault(logger)

	st, err := store.Open(cfg.Database.Path, logger)
	if err != nil {
		logger.Error("open database", "err", err)
		os.Exit(1)
	}

	rt := router.New()
	if err := rt.Reload(cfg); err != nil {
		logger.Error("invalid config", "err", err)
		os.Exit(1)
	}

	adminToken := resolveAdminToken(cfg, st, logger)
	bootstrapKey(st, logger)

	keys := keypool.NewManager()
	forwarder := transport.New(rt, st, keys, logger)

	srv := server.New(server.Options{
		Router:     rt,
		Forwarder:  forwarder,
		Store:      st,
		AdminToken: adminToken,
		MaxBody:    cfg.Server.MaxBodyBytes,
		Log:        logger,
	})

	httpSrv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.Value(),
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go watchConfig(ctx, *configPath, logger, rt)

	go func() {
		logger.Info("gateway listening",
			"addr", cfg.Server.Addr,
			"openai_base", "http://"+hostPort(cfg.Server.Addr)+"/openai/v1",
			"anthropic_base", "http://"+hostPort(cfg.Server.Addr)+"/anthropic",
			"providers", len(cfg.Providers))
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout.Value())
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	if err := st.Close(); err != nil {
		logger.Error("close database", "err", err)
	}
}

func newLogger(c config.Log) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(c.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if c.Format == "text" {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

// resolveAdminToken prefers config, then the database, then generates one.
func resolveAdminToken(cfg *config.Config, st *store.Store, log *slog.Logger) string {
	if cfg.Server.AdminToken != "" {
		return cfg.Server.AdminToken
	}
	if v, err := st.GetSetting("admin_token"); err == nil && v != "" {
		log.Info("admin token loaded from database; see admin-token.txt next to the database file")
		return v
	}
	buf := make([]byte, 24)
	_, _ = rand.Read(buf)
	token := hex.EncodeToString(buf)
	if err := st.SetSetting("admin_token", token); err != nil {
		log.Error("store admin token", "err", err)
	}
	path := filepath.Join(filepath.Dir(cfg.Database.Path), "admin-token.txt")
	_ = os.WriteFile(path, []byte(token+"\n"), 0o600)
	fmt.Printf("[BOOTSTRAP] admin token: %s\n", token)
	fmt.Printf("[BOOTSTRAP] also written to %s\n", path)
	return token
}

// bootstrapKey creates the first gateway key so the binary is usable at once.
func bootstrapKey(st *store.Store, log *slog.Logger) {
	n, err := st.CountKeys()
	if err != nil {
		log.Error("count gateway keys", "err", err)
		return
	}
	if n > 0 {
		return
	}
	plain, hash, prefix, err := auth.Generate()
	if err != nil {
		log.Error("generate gateway key", "err", err)
		return
	}
	if _, err := st.CreateKey("bootstrap", hash, prefix); err != nil {
		log.Error("create gateway key", "err", err)
		return
	}
	fmt.Printf("[BOOTSTRAP] first gateway key: %s\n", plain)
	fmt.Printf("[BOOTSTRAP] save it now; it is not shown again\n")
}

func watchConfig(ctx context.Context, path string, log *slog.Logger, rt *router.Router) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	lastMod := info.ModTime()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, err := os.Stat(path)
			if err != nil || !info.ModTime().After(lastMod) {
				continue
			}
			lastMod = info.ModTime()
			cfg, err := config.Load(path)
			if err != nil {
				log.Error("config reload failed; keeping previous config", "err", err)
				continue
			}
			if err := rt.Reload(cfg); err != nil {
				log.Error("config reload rejected; keeping previous config", "err", err)
				continue
			}
			log.Info("config reloaded", "providers", len(cfg.Providers))
		}
	}
}

func hostPort(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	return addr
}
