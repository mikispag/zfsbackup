package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mikispag/zfsbackup/internal/deleter"
	"github.com/mikispag/zfsbackup/internal/monitor"
	"github.com/mikispag/zfsbackup/internal/receiver"
	"github.com/mikispag/zfsbackup/internal/sender"
	"github.com/mikispag/zfsbackup/internal/snapshot"
)

func main() {
	flag.Parse()
	switch flag.Arg(0) {
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
		fmt.Fprintf(os.Stderr, "unknown command %q; expected one of: run receiver sender deleter snapshot monitor\n", flag.Arg(0))
		os.Exit(2)
	}
}
