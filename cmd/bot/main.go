package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"hl-carry-bot/internal/app"
	"hl-carry-bot/internal/config"
	"hl-carry-bot/internal/logging"

	"go.uber.org/zap"
)

func main() {
	configPath := flag.String("config", "internal/config/config.yaml", "path to config file")
	flag.Parse()

	if err := config.LoadEnv(".env"); err != nil {
		fmt.Fprintf(os.Stderr, "failed to load .env: %v\n", err)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		panic(err)
	}
	log := logging.New(cfg.Log)
	log.Info("config loaded", zap.String("path", *configPath))

	application, err := app.New(cfg, log)
	if err != nil {
		log.Error("failed to initialize app", zap.Error(err))
		os.Exit(1)
	}
	log.Info("app initialized")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := application.Run(ctx); err != nil && err != context.Canceled {
		log.Error("app terminated", zap.Error(err))
		os.Exit(1)
	}
}
