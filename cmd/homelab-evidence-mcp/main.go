// Command homelab-evidence-mcp lance le serveur MCP stdio en lecture seule.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/audit"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/mcpserver"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/version"
)

func main() {
	code := run(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}

// run est le point d’entrée testable avec stdout et stderr injectables.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("homelab-evidence-mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to YAML configuration file")
	showVersion := fs.Bool("version", false, "print version and exit")
	validateOnly := fs.Bool("validate", false, "load and validate config (and tokens/destinations) then exit")
	logLevel := fs.String("log-level", "info", "log level: debug, info, warn, error")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "error: unexpected positional arguments")
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintln(stdout, version.Version)
		return 0
	}
	if *configPath == "" {
		_, _ = fmt.Fprintln(stderr, "error: --config is required")
		return 2
	}

	level, ok := parseLevel(*logLevel)
	if !ok {
		_, _ = fmt.Fprintln(stderr, "error: --log-level must be debug, info, warn, or error")
		return 2
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := config.LoadFile(*configPath)
	if err != nil {
		log.Error("config invalid", "err", err)
		return 1
	}

	auditOut := io.Writer(stderr)
	var closer io.Closer
	if cfg.Audit.File != "" {
		rf, err := audit.OpenRotating(cfg.Audit.File, cfg.Audit.MaxBytes, cfg.Audit.MaxFiles)
		if err != nil {
			log.Error("audit file open failed", "err", err)
			return 1
		}
		closer = rf
		auditOut = audit.MultiWriter(stderr, rf)
	}
	if closer != nil {
		defer func() { _ = closer.Close() }()
	}
	aud := audit.New(auditOut)

	app, err := mcpserver.NewApp(cfg, log, aud)
	if err != nil {
		log.Error("startup failed", "err", err)
		return 1
	}

	if *validateOnly {
		log.Info("config ok", "version", version.Version, "services", app.Registry.Len(), "sources", len(cfg.Sources))
		_, _ = fmt.Fprintln(stdout, "config ok")
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("starting", "version", version.Version, "services", app.Registry.Len(), "sources", len(cfg.Sources))
	aud.Log(audit.Event{Action: "boot", Status: "ok", Detail: fmt.Sprintf("version=%s services=%d", version.Version, app.Registry.Len())})

	if err := mcpserver.RunStdio(ctx, app); err != nil &&
		!errors.Is(err, io.EOF) && ctx.Err() == nil {
		log.Error("server exited", "err", err)
		return 1
	}
	return 0
}

func parseLevel(s string) (slog.Level, bool) {
	switch s {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	default:
		return slog.LevelInfo, false
	}
}
