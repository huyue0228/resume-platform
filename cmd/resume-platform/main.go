package main

import (
	"context"
	"crypto/x509"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"resume-platform/internal/contract"
	"syscall"
	"time"

	"resume-platform/internal/platform"
	"resume-platform/internal/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("platform command failed", "detail", err.Error())
		os.Exit(1)
	}
}
func run() error {
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command == "check" {
		for _, name := range []string{"pdfinfo", "pdftotext", "pdfimages"} {
			if _, err := exec.LookPath(name); err != nil {
				return fmt.Errorf("missing runtime tool: %s", name)
			}
		}
		for _, name := range []string{"request", "response", "capabilities"} {
			raw, err := contract.Bundle.ReadFile("bundle/" + name + ".example.json")
			if err != nil {
				return err
			}
			if err = contract.Validate(name, raw); err != nil {
				return fmt.Errorf("contract self-check failed")
			}
		}
		roots, err := x509.SystemCertPool()
		if err != nil || len(roots.Subjects()) == 0 {
			return fmt.Errorf("CA trust store unavailable")
		}
		if err = web.Check(); err != nil {
			return fmt.Errorf("embedded React build unavailable")
		}
		slog.Info("Go runtime, PDF extraction tools, contracts and embedded frontend verified")
		return nil
	}
	if command == "healthcheck" {
		client := http.Client{Timeout: 3 * time.Second}
		address := os.Getenv("PLATFORM_HEALTH_URL")
		if address == "" {
			address = "http://127.0.0.1/healthz"
		}
		response, err := client.Get(address)
		if err != nil {
			return fmt.Errorf("health endpoint unavailable")
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return fmt.Errorf("platform is not ready")
		}
		return nil
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	app, err := platform.New(ctx, platform.ConfigFromEnv())
	if err != nil {
		return err
	}
	defer app.Close()
	switch command {
	case "init":
		if err = app.Migrate(ctx); err != nil {
			return fmt.Errorf("database migration failed: %s", platform.PublicError(err))
		}
		if err = app.Seed(ctx); err != nil {
			return fmt.Errorf("base data initialization failed: %s", platform.PublicError(err))
		}
		return nil
	case "migrate":
		if err = app.Migrate(ctx); err != nil {
			return fmt.Errorf("database migration failed: %s", platform.PublicError(err))
		}
		slog.Info("database migration complete")
		return nil
	case "seed":
		if err = app.Seed(ctx); err != nil {
			return fmt.Errorf("base data initialization failed: %s", platform.PublicError(err))
		}
		slog.Info("base data initialization complete")
		return nil
	case "issue-dev-token":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		username := flags.String("username", "", "employee number")
		if err = flags.Parse(os.Args[2:]); err != nil {
			return err
		}
		if *username == "" {
			return fmt.Errorf("--username is required")
		}
		value, err := app.IssueDevToken(ctx, *username)
		if err != nil {
			return fmt.Errorf("development session could not be issued")
		}
		fmt.Println(value)
		return nil
	case "serve":
		if os.Getenv("RUN_MIGRATIONS") != "0" {
			if err = app.Migrate(ctx); err != nil {
				return fmt.Errorf("database migration failed: %s", platform.PublicError(err))
			}
		}
		if os.Getenv("RUN_SEED_BASE") == "1" {
			if err = app.Seed(ctx); err != nil {
				return fmt.Errorf("base initialization failed")
			}
		}
		server := &http.Server{Addr: app.Config.Address, Handler: app.Handler(web.Handler()), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
		workerDone := make(chan struct{})
		go func() { defer close(workerDone); app.Workers(ctx) }()
		serverErr := make(chan error, 1)
		go func() {
			slog.Info("Go platform listening", "address", app.Config.Address)
			serverErr <- server.ListenAndServe()
		}()
		select {
		case err = <-serverErr:
			cancel()
		case <-ctx.Done():
		}
		shutdown, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		server.Shutdown(shutdown)
		cancel()
		<-workerDone
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("HTTP service failed")
		}
		return nil
	default:
		return fmt.Errorf("unknown command: use serve, init, migrate, seed, issue-dev-token, check or healthcheck")
	}
}
