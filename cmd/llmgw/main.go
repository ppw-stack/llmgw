package main

import (
	"context"
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
	"github.com/pw/llmgw/internal/router"
	"github.com/pw/llmgw/internal/secret"
	"github.com/pw/llmgw/internal/server"
	"github.com/pw/llmgw/internal/store"
	"github.com/pw/llmgw/internal/transport"
	"github.com/pw/llmgw/internal/webui"
)

func main() {
	var (
		configPath  = flag.String("config", "config.yaml", "path to config file")
		addr        = flag.String("addr", "", "override server.addr")
		showVersion = flag.Bool("version", false, "print version and exit")
		healthcheck = flag.Bool("healthcheck", false, "probe the local /healthz endpoint and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("llmgw %s (commit %s, built %s)\n", version, commit, date)
		return
	}
	if *healthcheck {
		os.Exit(runHealthcheck())
	}

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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.Database.Path, logger)
	if err != nil {
		logger.Error("open database", "err", err)
		os.Exit(1)
	}

	rt := router.New()

	box, err := masterKey(cfg, logger)
	if err != nil {
		logger.Error("master key", "err", err)
		_ = st.Close()
		os.Exit(1)
	}
	boxCipher, err := secret.New(box)
	if err != nil {
		logger.Error("init secret box", "err", err)
		_ = st.Close()
		os.Exit(1)
	}

	instances, models, err := st.Topology(boxCipher)
	if err != nil {
		logger.Error("load upstream topology", "err", err)
		_ = st.Close()
		os.Exit(1)
	}
	if len(instances) == 0 && len(cfg.Instances) > 0 {
		if err := st.ImportTopology(cfg.Instances, cfg.Models, boxCipher); err != nil {
			logger.Error("import upstream topology from config", "err", err)
			_ = st.Close()
			os.Exit(1)
		}
		instances, models, err = st.Topology(boxCipher)
		if err != nil {
			logger.Error("reload upstream topology", "err", err)
			_ = st.Close()
			os.Exit(1)
		}
		logger.Info("imported upstream topology from config.yaml",
			"instances", len(instances), "models", len(models))
	}
	effective := *cfg
	effective.Instances = instances
	effective.Models = models

	if err := rt.Reload(&effective); err != nil {
		logger.Error("invalid config", "err", err)
		_ = st.Close()
		os.Exit(1)
	}

	adminUsername := ensureAdmin(cfg, st, logger)
	adminToken := legacyAdminToken(cfg, st, logger)
	bootstrapKey(st, logger)

	forwarder := transport.NewWithTimeout(rt, st, logger, cfg.UpstreamTimeout.Value())
	// Refuse to dial loopback and private addresses unless an instance opted in
	// with allow_private_upstream. This is what keeps a stolen admin session
	// from turning the gateway into an internal-network proxy.
	forwarder.SetBlockPrivate(true)

	srv, err := server.New(server.Options{
		Router:           rt,
		Forwarder:        forwarder,
		Store:            st,
		Box:              boxCipher,
		AdminToken:       adminToken,
		MetricsToken:     cfg.Server.MetricsToken,
		SessionTTL:       cfg.Server.SessionTTL.Value(),
		SecureCookie:     cfg.Server.SecureCookie,
		TrustedProxies:   cfg.Server.TrustedPrefixes,
		AdminPrefixes:    cfg.Server.AdminPrefixes,
		AdminEnabled:     cfg.Server.AdminEnabled,
		KeyRPM:           cfg.Server.KeyRPM,
		KeyMaxConcurrent: cfg.Server.KeyMaxConcurrent,
		MaxBody:          cfg.Server.MaxBodyBytes,
		Log:              logger,
		UI:               webui.Handler(),
	})
	if err != nil {
		logger.Error("initialize server", "err", err)
		_ = st.Close()
		os.Exit(1)
	}

	httpSrv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.Value(),
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	if err := st.DeleteExpiredSessions(); err != nil {
		logger.Warn("clean expired sessions", "err", err)
	}
	go cleanupSessions(ctx, st, logger)
	if days := cfg.Database.RetentionDays; days > 0 {
		go pruneHistory(ctx, st, logger, days)
	}
	warnInsecureDefaults(cfg, logger)
	go watchConfig(ctx, *configPath, logger, func(c *config.Config) error {
		return reloadConfig(rt, st, boxCipher, srv, c)
	})

	go func() {
		logger.Info("gateway listening",
			"addr", cfg.Server.Addr,
			"openai_base", "http://"+hostPort(cfg.Server.Addr)+"/openai/v1",
			"anthropic_base", "http://"+hostPort(cfg.Server.Addr)+"/anthropic",
			"instances", len(effective.Instances),
			"models", len(effective.Models),
			"admin_user", adminUsername)
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

// ensureAdmin makes sure the configured admin user exists. When no password is
// configured and the user is new, a random password is generated and printed
// exactly once.
func ensureAdmin(cfg *config.Config, st *store.Store, log *slog.Logger) string {
	username := cfg.Server.AdminUsername
	if username == "" {
		username = "admin"
	}
	user, err := st.UserByName(username)
	if err == nil {
		if cfg.Server.AdminPassword != "" && !auth.CheckPassword(user.PasswordHash, cfg.Server.AdminPassword) {
			hash, herr := auth.HashPassword(cfg.Server.AdminPassword)
			if herr != nil {
				log.Error("hash admin password", "err", herr)
			} else if uerr := st.UpdatePassword(username, hash); uerr != nil {
				log.Error("update admin password", "err", uerr)
			} else {
				// A password reset must invalidate every existing session.
				_ = st.DeleteSessionsForUser(username)
				log.Info("admin password reset from config; all sessions revoked", "username", username)
			}
		}
		return username
	}
	if !errors.Is(err, store.ErrNotFound) {
		log.Error("lookup admin user", "err", err)
		return username
	}

	password := cfg.Server.AdminPassword
	generated := false
	if password == "" {
		password, err = auth.RandomPassword(24)
		if err != nil {
			log.Error("generate admin password", "err", err)
			return username
		}
		generated = true
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		log.Error("hash admin password", "err", err)
		return username
	}
	if _, err := st.CreateUser(username, hash); err != nil {
		log.Error("create admin user", "err", err)
		return username
	}
	if !generated {
		log.Info("admin user created from config", "username", username)
		return username
	}
	path := filepath.Join(filepath.Dir(cfg.Database.Path), "admin-password.txt")
	_ = os.WriteFile(path, []byte(password+"\n"), 0o600)
	fmt.Printf("[BOOTSTRAP] admin username: %s\n", username)
	fmt.Printf("[BOOTSTRAP] admin password: %s\n", password)
	fmt.Printf("[BOOTSTRAP] also written to %s\n", path)
	return username
}

// legacyAdminToken keeps the old Bearer token working, but only when it was
// explicitly configured or already stored by a previous version.
func legacyAdminToken(cfg *config.Config, st *store.Store, log *slog.Logger) string {
	if cfg.Server.AdminToken != "" {
		return cfg.Server.AdminToken
	}
	if v, err := st.GetSetting("admin_token"); err == nil && v != "" {
		log.Warn("legacy admin_token found in database and still accepted as a Bearer token; " +
			"set server.admin_token to keep it or remove the setting to disable it")
		return v
	}
	return ""
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

// masterKey returns the AES key used to encrypt upstream provider keys.
func masterKey(cfg *config.Config, log *slog.Logger) ([]byte, error) {
	if cfg.Server.MasterKey != "" {
		return secret.ParseKey(cfg.Server.MasterKey)
	}
	path := filepath.Join(filepath.Dir(cfg.Database.Path), "master.key")
	_, statErr := os.Stat(path)
	key, err := secret.LoadOrCreateKey(path)
	if err != nil {
		return nil, err
	}
	if os.IsNotExist(statErr) {
		log.Warn("generated a new master key; back it up or stored upstream keys cannot be decrypted",
			"path", path)
	}
	return key, nil
}

// cleanupSessions periodically removes expired admin sessions.
func cleanupSessions(ctx context.Context, st *store.Store, log *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := st.DeleteExpiredSessions(); err != nil {
				log.Warn("clean expired sessions", "err", err)
			}
		}
	}
}

// pruneStartupDelay is the pause between the start of the gateway and the
// first cleanup. Right after the boot the gateway is still warming up, and a
// large cleanup would take the write lock away from the first requests.
const pruneStartupDelay = 30 * time.Second

// pruneHistory keeps the request log, the audit log and the daily usage table
// from growing forever. It runs once shortly after the startup and then daily.
func pruneHistory(ctx context.Context, st *store.Store, log *slog.Logger, days int) {
	prune := func() {
		cutoff := time.Now().UTC().AddDate(0, 0, -days)
		res, err := st.Prune(cutoff)
		if err != nil {
			log.Warn("prune history", "err", err)
			return
		}
		if res.RequestLogs+res.AuditLogs+res.UsageDays > 0 {
			log.Info("pruned history",
				"request_logs", res.RequestLogs, "audit_logs", res.AuditLogs,
				"usage_days", res.UsageDays, "retention_days", days)
		}
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(pruneStartupDelay):
	}
	prune()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}

// warnInsecureDefaults points out the settings that are safe on a laptop and
// dangerous on the public internet. It never refuses to start: the operator may
// deliberately run behind a network boundary.
func warnInsecureDefaults(cfg *config.Config, log *slog.Logger) {
	if cfg.Server.AdminPassword != "" && len(cfg.Server.AdminPassword) < 12 {
		log.Warn("server.admin_password is shorter than 12 characters; use a long random password on a reachable host")
	}
	if len(cfg.Server.AdminPrefixes) == 0 {
		log.Warn("server.admin_allowed_cidrs is empty: the admin console is reachable from anywhere the listener is; " +
			"restrict it by network policy or set admin_allowed_cidrs")
	}
	if cfg.Server.MetricsToken == "" {
		log.Warn("server.metrics_token is empty: /metrics is readable only from localhost and trusted proxies; " +
			"set a token to scrape it from elsewhere")
	}
	if cfg.Server.AdminToken != "" {
		log.Warn("server.admin_token is configured: it is a static, never-expiring credential with full admin rights")
	}
	if cfg.Server.KeyRPM == 0 && cfg.Server.KeyMaxConcurrent == 0 {
		log.Warn("no per-key limits are configured (key_rpm / key_max_concurrent): one leaked gateway key can spend upstream quota without bound")
	}
	if !cfg.Server.SecureCookie {
		log.Warn("server.secure_cookie is false: the admin session cookie is only marked Secure when the request itself is HTTPS")
	}
	for _, inst := range cfg.Instances {
		for _, ep := range inst.Endpoints {
			if strings.HasPrefix(strings.ToLower(ep.BaseURL), "http://") {
				log.Warn("upstream endpoint uses plain HTTP; the upstream key travels in clear text",
					"instance", inst.Name, "base_url", ep.BaseURL)
			}
		}
	}
	for _, inst := range cfg.Instances {
		if inst.AllowPrivateUpstream {
			log.Warn("instance allows private/loopback upstreams", "instance", inst.Name)
		}
	}
}

// reloadConfig applies a freshly loaded configuration to the running gateway.
//
// The routing table is swapped first and only a successful swap moves the
// management surface, so a configuration the gateway rejects leaves both
// untouched.
func reloadConfig(rt *router.Router, st *store.Store, box *secret.Box, srv *server.Server,
	c *config.Config) error {
	dbInstances, dbModels, err := st.Topology(box)
	if err != nil {
		return fmt.Errorf("load topology: %w", err)
	}
	if len(dbInstances) > 0 {
		c.Instances = dbInstances
		c.Models = dbModels
	}
	if err := rt.Reload(c); err != nil {
		return fmt.Errorf("reload routing table: %w", err)
	}
	// The runtime policy follows the config: the management surface, its
	// network whitelist and the per-key budget. Changing them needs no
	// restart: edit the file and they are in effect within a second.
	srv.SetAdminEnabled(c.Server.AdminIsEnabled())
	srv.SetAdminPrefixes(c.Server.AdminPrefixes)
	srv.SetKeyLimits(c.Server.KeyRPM, c.Server.KeyMaxConcurrent)
	return nil
}

func watchConfig(ctx context.Context, path string, log *slog.Logger, apply func(*config.Config) error) {
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
			if err := apply(cfg); err != nil {
				log.Error("config reload rejected; keeping previous config", "err", err)
				continue
			}
			log.Info("config reloaded", "instances", len(cfg.Instances), "models", len(cfg.Models))
		}
	}
}

func hostPort(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	return addr
}

// version metadata, injected at build time via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// runHealthcheck lets a distroless container probe itself without a shell.
func runHealthcheck() int {
	url := os.Getenv("LLMGW_HEALTHCHECK_URL")
	if url == "" {
		url = "http://127.0.0.1:8080/healthz"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}
