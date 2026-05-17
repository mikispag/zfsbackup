package main

import (
	"fmt"
	"os"

	"github.com/mikispag/zfsbackup/internal/deleter"
	"github.com/mikispag/zfsbackup/internal/monitor"
	"github.com/mikispag/zfsbackup/internal/receiver"
	"github.com/mikispag/zfsbackup/internal/sender"
	"github.com/mikispag/zfsbackup/internal/snapshot"
)

// isTerminal reports whether f is connected to a terminal.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func printHelp() {
	color := isTerminal(os.Stdout) && os.Getenv("NO_COLOR") == ""

	bold := func(s string) string {
		if color {
			return "\033[1m" + s + "\033[0m"
		}
		return s
	}
	cyan := func(s string) string {
		if color {
			return "\033[1;36m" + s + "\033[0m"
		}
		return s
	}
	yellow := func(s string) string {
		if color {
			return "\033[1;33m" + s + "\033[0m"
		}
		return s
	}
	dim := func(s string) string {
		if color {
			return "\033[2m" + s + "\033[0m"
		}
		return s
	}

	fmt.Printf("%s  %s\n\n",
		cyan("zfsbackup"),
		dim("· automated incremental ZFS backups over SSH"),
	)

	fmt.Printf("%s\n", yellow("USAGE"))
	fmt.Printf("  zfsbackup <command> [flags]\n\n")

	fmt.Printf("%s\n", yellow("COMMANDS"))
	commands := [][2]string{
		{"run", "Run all configured modules in sequence from a single config file"},
		{"snapshot", "Create ZFS snapshots on a schedule"},
		{"sender", "Stream incremental snapshots to one or more remote receivers"},
		{"deleter", "Prune old snapshots with configurable retention rules"},
		{"monitor", "Export snapshot freshness metrics for Prometheus"},
		{"receiver", "Accept snapshot streams (deployed as SSH ForceCommand)"},
	}
	for _, c := range commands {
		fmt.Printf("  %-12s%s\n", bold(c[0]), c[1])
	}
	fmt.Println()

	fmt.Printf("%s\n", yellow("RUN"))
	fmt.Printf("  %-32s%s\n", bold("--config")+"=PATH", "Unified config file (required)")
	fmt.Printf("  %-32s%s\n", bold("--dry-run"), "Dry-run mode for snapshot and deleter")
	fmt.Printf("  %-32s%s\n", bold("--parallelism")+"=N", "Filesystems to process in parallel  "+dim("[default: 1]"))
	fmt.Printf("  %-32s%s\n", bold("--debug"), "Debug logging")
	fmt.Println()

	fmt.Printf("%s\n", yellow("SNAPSHOT"))
	fmt.Printf("  %-32s%s\n", bold("--config")+"=PATH", "Config file (required)")
	fmt.Printf("  %-32s%s\n", bold("--dry-run"), "Print snapshot commands without executing")
	fmt.Printf("  %-32s%s\n", bold("--debug"), "Debug logging")
	fmt.Println()

	fmt.Printf("%s\n", yellow("SENDER"))
	fmt.Printf("  %-32s%s\n", bold("--config")+"=PATH", "Config file (required)")
	fmt.Printf("  %-32s%s\n", bold("--limit-fs")+"=DATASET", "Restrict to a single filesystem")
	fmt.Printf("  %-32s%s\n", bold("--parallelism")+"=N", "Filesystems to process in parallel  "+dim("[default: 1]"))
	fmt.Printf("  %-32s%s\n", bold("--debug"), "Debug logging")
	fmt.Println()

	fmt.Printf("%s\n", yellow("DELETER"))
	fmt.Printf("  %-32s%s\n", bold("--config")+"=PATH", "Config file (required)")
	fmt.Printf("  %-32s%s\n", bold("--dry-run"), "Print deletions without executing  "+dim("[default: true]"))
	fmt.Printf("  %-32s%s\n", bold("--parallelism")+"=N", "Filesystems to process in parallel  "+dim("[default: 1]"))
	fmt.Printf("  %-32s%s\n", bold("--debug"), "Debug logging")
	fmt.Println()

	fmt.Printf("%s\n", yellow("MONITOR"))
	fmt.Printf("  %-32s%s\n", bold("--config")+"=PATH", "Config file (required)")
	fmt.Printf("  %-32s%s\n", bold("--debug"), "Debug logging")
	fmt.Println()

	fmt.Printf("%s  %s\n", yellow("RECEIVER"), dim("(SSH ForceCommand — not invoked manually)"))
	fmt.Printf("  %-32s%s\n", bold("--config")+"=PATH", "Receiver config file (optional)")
	fmt.Printf("  %-32s%s\n", bold("--base_dataset")+"=DATASET", "Override root destination dataset")
	fmt.Printf("  %-32s%s\n", bold("--debug"), "Debug logging")
	fmt.Println()

	fmt.Printf("%s\n", yellow("EXAMPLES"))
	fmt.Printf("  %s  %s\n",
		bold("zfsbackup run"),
		"--config /etc/zfsbackup/mypool.json",
	)
	fmt.Printf("  %s  %s\n",
		bold("zfsbackup sender"),
		"--config /etc/zfsbackup/mypool.json --limit-fs tank/important",
	)
	fmt.Printf("  %s  %s\n",
		bold("zfsbackup deleter"),
		"--config /etc/zfsbackup/mypool.json --dry-run=false",
	)
	fmt.Println()
	fmt.Printf("  %s\n", dim("https://github.com/mikispag/zfsbackup"))
	fmt.Println()
}

func main() {
	// Check for --help / -h before any flag parsing so we control the output.
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(0)
	}
	for _, arg := range os.Args[1:] {
		if arg == "--help" || arg == "-h" || arg == "help" {
			printHelp()
			os.Exit(0)
		}
	}

	switch os.Args[1] {
	case "run":
		runMain()
	case "receiver":
		receiver.Main()
	case "sender":
		sender.Main()
	case "deleter":
		deleter.Main()
	case "snapshot":
		snapshot.Main()
	case "monitor":
		monitor.Main()
	default:
		fmt.Fprintf(os.Stderr, "zfsbackup: unknown command %q\nRun 'zfsbackup --help' for usage.\n", os.Args[1])
		os.Exit(2)
	}
}
