package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// healthcheck lets a container with no shell or curl probe itself.
func healthcheck(listen string) int {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func main() {
	check := flag.Bool("healthcheck", false, "probe the running server and exit 0 if healthy")
	flag.Parse()

	listen := env("LISTEN", ":8080")
	if *check {
		os.Exit(healthcheck(listen))
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	vault := env("VAULT_DIR", "/vault")
	interval, err := time.ParseDuration(env("SCAN_INTERVAL", "5s"))
	if err != nil || interval < time.Second {
		log.Error("SCAN_INTERVAL must be a duration of at least 1s", "value", os.Getenv("SCAN_INTERVAL"))
		os.Exit(2)
	}

	a := newApp()
	sc := newScanner(vault, log, &a.snap)
	if _, err := sc.scan(); err != nil {
		log.Error("initial vault scan failed", "vault", vault, "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              listen,
		Handler:           a,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				// On failure (e.g. the vault mount vanished) keep serving the last snapshot.
				if _, err := sc.scan(); err != nil {
					log.Error("vault scan failed; keeping previous index", "err", err)
				}
			}
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Info("listening", "addr", listen, "vault", vault, "scan_interval", interval.String())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server error", "err", err)
		os.Exit(1)
	}
}
