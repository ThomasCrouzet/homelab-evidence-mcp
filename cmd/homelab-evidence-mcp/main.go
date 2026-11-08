// Command homelab-evidence-mcp starts the read-only MCP stdio server.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/audit"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/mcpserver"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/redaction"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/version"
)

func main() {
	code := run(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}

// Tests use run with specified stdout and stderr writers.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("homelab-evidence-mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to YAML configuration file")
	showVersion := fs.Bool("version", false, "print version and exit")
	validateOnly := fs.Bool("validate", false, "load and validate config (and tokens/destinations) then exit")
	doctor := fs.Bool("doctor", false, "run bounded read-only source diagnostics then exit")
	jsonOutput := fs.Bool("json", false, "use JSON output with --doctor")
	logLevel := fs.String("log-level", "info", "log level: debug, info, warn, error")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "error: you cannot use positional arguments")
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintln(stdout, version.Version)
		return 0
	}
	if *configPath == "" {
		_, _ = fmt.Fprintln(stderr, "error: --config is necessary")
		return 2
	}
	if *jsonOutput && !*doctor {
		_, _ = fmt.Fprintln(stderr, "error: --json is only valid with --doctor")
		return 2
	}
	if *doctor && *validateOnly {
		_, _ = fmt.Fprintln(stderr, "error: use only one of --validate or --doctor")
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
		return startupError(startupMode(*doctor, *validateOnly), *jsonOutput, stdout, stderr, log, "configuration_error", err)
	}

	auditOut := io.Writer(stderr)
	var closer io.Closer
	if !*validateOnly && !*doctor && cfg.Audit.File != "" {
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
	aud := audit.New(auditOut, stderr)

	app, err := mcpserver.NewApp(cfg, log, aud)
	if err != nil {
		return startupError(startupMode(*doctor, *validateOnly), *jsonOutput, stdout, stderr, log, "startup_error", err)
	}

	if *validateOnly {
		log.Info("config ok", "version", version.Version, "services", app.Registry.Len(), "sources", len(cfg.Sources))
		_, _ = fmt.Fprintln(stdout, "config ok")
		return 0
	}

	if *doctor {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		report := app.Doctor(ctx)
		if *jsonOutput {
			encoder := json.NewEncoder(stdout)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(report); err != nil {
				log.Error("doctor output failed", "err", err)
				return 1
			}
		} else {
			writeDoctorHuman(stdout, report)
		}
		if !report.Complete {
			return 1
		}
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

var startupPathRe = regexp.MustCompile(`(^|[\s"'(=])((?:[A-Za-z]:\\|/)[^\s"':,)]+)`)

// startupError sanitizes a failure before the application redaction engine is
// available. It keeps a category and field names, not local paths or secrets.
func startupMode(doctor, validate bool) string {
	switch {
	case doctor:
		return "doctor"
	case validate:
		return "validate"
	default:
		return "startup"
	}
}

func startupError(mode string, jsonOutput bool, stdout, stderr io.Writer, log *slog.Logger, reason string, err error) int {
	message := sanitizeStartupError(err)
	switch mode {
	case "doctor":
		if jsonOutput {
			encoder := json.NewEncoder(stdout)
			encoder.SetIndent("", "  ")
			_ = encoder.Encode(map[string]string{"error": message, "reason": reason})
		} else {
			_, _ = fmt.Fprintf(stderr, "doctor error: %s\n", message)
		}
		return 2
	case "validate":
		_, _ = fmt.Fprintf(stderr, "error: %s\n", message)
		return 2
	default:
		if reason == "configuration_error" {
			_, _ = fmt.Fprintf(stderr, "error: %s\n", message)
		} else {
			log.Error("startup failed", "err", message)
		}
		return 1
	}
}

func sanitizeStartupError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if engine, engineErr := redaction.New(nil); engineErr == nil {
		message, _ = engine.Apply(message)
	}
	message = startupPathRe.ReplaceAllString(message, "${1}[path]")
	message, _ = redaction.Truncate(message, 300)
	return message
}

func writeDoctorHuman(out io.Writer, report mcpserver.DoctorReport) {
	_, _ = fmt.Fprintf(out, "doctor: version=%s readonly=%v sources=%d services=%d complete=%v\n",
		report.Version, report.ReadOnly, len(report.Sources), report.ServicesCount, report.Complete)
	for _, source := range report.Sources {
		_, _ = fmt.Fprintf(out, "source %s (%s): status=%s reason=%s complete=%v capabilities=%s\n",
			source.Name, source.Kind, source.Status, source.Reason, source.Complete,
			strings.Join(source.Capabilities, ","))
		for _, binding := range source.Bindings {
			_, _ = fmt.Fprintf(out, "  service %s: status=%s reason=%s complete=%v items=%d\n",
				binding.ServiceID, binding.Status, binding.Reason, binding.Complete, binding.ItemCount)
		}
	}
	for _, warning := range report.Warnings {
		_, _ = fmt.Fprintf(out, "warning: %s\n", warning)
	}
	_, _ = fmt.Fprintln(out, mcpserver.DoctorSummary(report))
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
