package main

import (
	"fmt"
	"os"
	"strings"

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

	// pad returns spaces so that visible text of width visLen aligns to col.
	pad := func(visLen, col int) string {
		n := col - visLen
		if n < 1 {
			n = 1
		}
		return strings.Repeat(" ", n)
	}

	// cmdLine prints "  <bold name><padding><description>" where padding is
	// computed from the name's visible length, not its escape-inflated length.
	cmdLine := func(name, desc string) {
		fmt.Printf("  %s%s%s\n", bold(name), pad(len(name), 12), desc)
	}

	// flagLine prints "  <bold --flag>[=TYPE]<padding><description>".
	// The flag name is bolded; the =TYPE suffix is plain; padding is based on
	// the total visible width (flag + =TYPE).
	flagLine := func(nameAndType, desc string) {
		parts := strings.SplitN(nameAndType, "=", 2)
		display := bold(parts[0])
		if len(parts) > 1 {
			display += "=" + parts[1]
		}
		fmt.Printf("  %s%s%s\n", display, pad(len(nameAndType), 30), desc)
	}

	fmt.Printf("%s  %s\n\n",
		cyan("zfsbackup"),
		dim("· automated incremental ZFS backups over SSH"),
	)

	fmt.Printf("%s\n", yellow("USAGE"))
	fmt.Printf("  zfsbackup <command> [flags]\n\n")

	fmt.Printf("%s\n", yellow("COMMANDS"))
	cmdLine("run", "Run all configured modules in sequence from a single config file")
	cmdLine("snapshot", "Create ZFS snapshots on a schedule")
	cmdLine("sender", "Stream incremental snapshots to one or more remote receivers")
	cmdLine("deleter", "Prune old snapshots with configurable retention rules")
	cmdLine("monitor", "Export snapshot freshness metrics for Prometheus")
	cmdLine("receiver", "Accept snapshot streams (deployed as SSH ForceCommand)")
	fmt.Println()

	fmt.Printf("%s\n", yellow("RUN"))
	flagLine("--config=PATH", "Unified config file (required)")
	flagLine("--dry-run", "Dry-run mode for snapshot and deleter")
	flagLine("--parallelism=N", "Filesystems to process in parallel  "+dim("[default: 1]"))
	flagLine("--debug", "Debug logging")
	fmt.Println()

	fmt.Printf("%s\n", yellow("SNAPSHOT"))
	flagLine("--config=PATH", "Config file (required)")
	flagLine("--dry-run", "Print snapshot commands without executing")
	flagLine("--debug", "Debug logging")
	fmt.Println()

	fmt.Printf("%s\n", yellow("SENDER"))
	flagLine("--config=PATH", "Config file (required)")
	flagLine("--limit-fs=DATASET", "Restrict to a single filesystem")
	flagLine("--parallelism=N", "Filesystems to process in parallel  "+dim("[default: 1]"))
	flagLine("--debug", "Debug logging")
	fmt.Println()

	fmt.Printf("%s\n", yellow("DELETER"))
	flagLine("--config=PATH", "Config file (required)")
	flagLine("--dry-run", "Print deletions without executing  "+dim("[default: true]"))
	flagLine("--parallelism=N", "Filesystems to process in parallel  "+dim("[default: 1]"))
	flagLine("--debug", "Debug logging")
	fmt.Println()

	fmt.Printf("%s\n", yellow("MONITOR"))
	flagLine("--config=PATH", "Config file (required)")
	flagLine("--debug", "Debug logging")
	fmt.Println()

	fmt.Printf("%s  %s\n", yellow("RECEIVER"), dim("(SSH ForceCommand — not invoked manually)"))
	flagLine("--config=PATH", "Receiver config file (optional)")
	flagLine("--base_dataset=DATASET", "Override root destination dataset")
	flagLine("--debug", "Debug logging")
	fmt.Println()

	fmt.Printf("%s\n", yellow("EXAMPLES"))
	fmt.Printf("  %s  --config /etc/zfsbackup/mypool.json\n", bold("zfsbackup run"))
	fmt.Printf("  %s  --config /etc/zfsbackup/mypool.json --limit-fs tank/important\n", bold("zfsbackup sender"))
	fmt.Printf("  %s  --config /etc/zfsbackup/mypool.json --dry-run=false\n", bold("zfsbackup deleter"))
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
