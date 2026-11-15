// Command mcp-piratebay is the OpenPlayX torrent search service: a /v1 HTTP
// API and an MCP endpoint over one administrator-configured index.
//
//	mcp-piratebay --config config.json --secrets secrets.json [--host H] [--port P]
//	mcp-piratebay version
//	mcp-piratebay healthcheck URL    exit 0 when URL answers 200 (Docker HEALTHCHECK)
//
// Environment: TORRENT_SEARCH_CONFIG / TORRENT_SEARCH_SECRETS (defaults for
// the flags).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/openplayx/mcp-piratebay/internal/auth"
	"github.com/openplayx/mcp-piratebay/internal/config"
	"github.com/openplayx/mcp-piratebay/internal/netguard"
	"github.com/openplayx/mcp-piratebay/internal/server"
	"github.com/openplayx/mcp-piratebay/internal/service"
	"github.com/openplayx/mcp-piratebay/internal/source"
	"github.com/openplayx/mcp-piratebay/internal/store"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Println(version)
			return
		case "healthcheck":
			os.Exit(healthcheck(os.Args[2:]))
		}
	}
	configPath := flag.String("config", os.Getenv("TORRENT_SEARCH_CONFIG"), "config.json path")
	secretsPath := flag.String("secrets", os.Getenv("TORRENT_SEARCH_SECRETS"), "secrets.json path")
	host := flag.String("host", "", "overrides server.host")
	port := flag.Int("port", 0, "overrides server.port")
	flag.Parse()
	if err := run(*configPath, *secretsPath, *host, *port); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func logLevel(name string) slog.Level {
	switch name {
	case "DEBUG":
		return slog.LevelDebug
	case "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	}
	return slog.LevelInfo
}

func run(configPath, secretsPath, host string, port int) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	secrets, err := config.LoadSecrets(secretsPath)
	if err != nil {
		return err
	}
	proxy, err := config.ResolveProxy(cfg, secrets)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel(cfg.Server.LogLevel)}))
	slog.SetDefault(log)
	st, err := store.Open(cfg.Cache.Path, cfg.Cache.MaxEntries)
	if err != nil {
		return err
	}
	defer st.Close()
	src, err := source.New(cfg.Source, proxy, nil, netguard.SystemResolver)
	if err != nil {
		return err
	}
	svc := service.New(cfg, src, st, log)
	if len(secrets.Tokens) == 0 {
		log.Warn("no API tokens configured; every /v1 and /mcp request will be rejected")
	}
	if host == "" {
		host = cfg.Server.Host
	}
	if port == 0 {
		port = cfg.Server.Port
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	srv := &http.Server{
		Addr:              addr,
		Handler:           server.New(svc, auth.New(secrets, cfg.Limits.RequestsPerMinute), log, version),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr, "version", version, "source", cfg.Source.ID)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

func healthcheck(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: mcp-piratebay healthcheck URL")
		return 2
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
