package main

import (
	"errors"
	"flag"
	"log/slog"
	"os"

	"github.com/mikispag/zfsbackup/internal/config"
	"github.com/mikispag/zfsbackup/internal/deleter"
	"github.com/mikispag/zfsbackup/internal/monitor"
	"github.com/mikispag/zfsbackup/internal/sender"
	"github.com/mikispag/zfsbackup/internal/snapshot"
	"github.com/mikispag/zfsbackup/internal/zfs"
)

// runMain implements the "run" subcommand which runs all configured modules in
// sequence: snapshot → deleter → sender → monitor. Modules whose section is
// absent from the config are silently skipped. All modules run regardless of
// individual failures; errors are collected and reported together at the end.
func runMain() {
	runFlags := flag.NewFlagSet("run", flag.ExitOnError)
	configFile := runFlags.String("config", "", "path to unified config file")
	parallelism := runFlags.Int("parallelism", 1, "number of filesystems to process in parallel")
	dryRun := runFlags.Bool("dry-run", false, "pass dry-run flag to snapshot and deleter")
	debug := runFlags.Bool("debug", false, "enable debug logging")
	runFlags.Parse(os.Args[2:])
	zfs.SetupLogger(*debug)

	if *configFile == "" {
		zfs.Fatal("--config is required")
	}

	cfg := &config.Config{}
	zfs.LoadConfig(*configFile, cfg)

	var errs []error

	if cfg.Snapshot != nil {
		slog.Info("running snapshot module")
		if err := snapshot.Run(cfg, *dryRun); err != nil {
			slog.Error("snapshot failed", "err", err)
			errs = append(errs, err)
		}
	}

	if cfg.Deleter != nil {
		slog.Info("running deleter module")
		if err := deleter.Run(cfg, *parallelism, *dryRun); err != nil {
			slog.Error("deleter failed", "err", err)
			errs = append(errs, err)
		}
	}

	if cfg.Sender != nil {
		slog.Info("running sender module")
		if err := sender.Run(cfg, *parallelism, ""); err != nil {
			slog.Error("sender failed", "err", err)
			errs = append(errs, err)
		}
	}

	if cfg.Monitor != nil {
		slog.Info("running monitor module")
		if err := monitor.Run(cfg); err != nil {
			slog.Error("monitor failed", "err", err)
			errs = append(errs, err)
		}
	}

	if err := errors.Join(errs...); err != nil {
		zfs.Fatal("run completed with errors", "err", err)
	}
}
