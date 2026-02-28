package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/marcelocantos/dais/internal/manager"
	"github.com/marcelocantos/dais/internal/server"
)

var version = "dev"

func main() {
	port := flag.Int("port", 8080, "listen port")
	workDir := flag.String("workdir", ".", "default working directory for sessions")
	model := flag.String("model", "", "default model for sessions (e.g. opus, sonnet)")
	debug := flag.Bool("debug", false, "enable debug logging")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("daisd", version)
		os.Exit(0)
	}

	logLevel := slog.LevelInfo
	if *debug {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	})))

	mgr := manager.New(*model, *workDir)
	srv := server.New(mgr, version)

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("shutting down", "signal", sig)
		os.Exit(0)
	}()

	addr := fmt.Sprintf(":%d", *port)
	slog.Info("daisd starting", "addr", addr, "version", version)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}
