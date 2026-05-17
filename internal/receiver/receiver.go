package receiver

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"

	"github.com/mikispag/zfsbackup/internal/config"
	"github.com/mikispag/zfsbackup/internal/zfs"
)

func setPlaceholders(baseds, ds string) {
	spBytes, err := io.ReadAll(os.Stdin)
	zfs.FatalIfError(err, "cannot read from stdin: %w")
	sp := &config.SetPlaceholders{}
	err = json.Unmarshal(spBytes, sp)
	zfs.FatalIfError(err, "cannot decode SetPlaceholders: %w")

	interestingGuids := make(map[string]bool, len(sp.Placeholders))
	for _, p := range sp.Placeholders {
		interestingGuids[p.GUID] = true
	}
	if err := zfs.IsValidZFSDataset(sp.FS); err != nil {
		zfs.Fatal("invalid filesystem in SetPlaceholders payload", "err", err)
	}
	if sp.FS != ds {
		zfs.Fatal("filesystem disagreement between payload and argument",
			"payload_fs", sp.FS, "arg_ds", ds)
	}

	destinationDs := fmt.Sprintf("%s/%s", baseds, sp.FS)
	snaps, err := zfs.ZfsList([]string{"name", "guid"}, "snapshot", destinationDs)
	zfs.FatalIfError(err, "cannot list snapshots: %w")
	bms, err := zfs.ZfsList([]string{"name", "guid"}, "bookmark", destinationDs)
	zfs.FatalIfError(err, "cannot list bookmarks: %w")

	byGuidSnaps := make(map[string][]string)
	byGuidBms := make(map[string][]string)
	for _, row := range snaps {
		if interestingGuids[row[1]] {
			byGuidSnaps[row[1]] = append(byGuidSnaps[row[1]], row[0])
		}
	}
	for _, row := range bms {
		if interestingGuids[row[1]] {
			byGuidBms[row[1]] = append(byGuidBms[row[1]], row[0])
		}
	}

	var errs []string
PLACEHOLDERS:
	for _, p := range sp.Placeholders {
		guid := p.GUID
		if len(byGuidBms[guid]) == 0 && len(byGuidSnaps[guid]) == 0 {
			errs = append(errs, fmt.Sprintf("no snapshot/bookmark for %v", p))
			continue
		}
		for _, b := range byGuidBms[guid] {
			if strings.HasSuffix(b, "-"+p.Name) {
				continue PLACEHOLDERS
			}
		}
		items := append(byGuidSnaps[guid], byGuidBms[guid]...)
		if err := zfs.ZfsSetPlaceholder(items[0], p.Name); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		zfs.MyFatalFnF("placeholder errors: %s", strings.Join(errs, "; "))
	}
}

func incrementalSuggestion(baseds, ds string) {
	destinationDs := fmt.Sprintf("%s/%s", baseds, ds)
	outp, err := zfs.ZfsList([]string{"name", "receive_resume_token"}, "filesystem", destinationDs)
	ret := &config.IncrementalSuggestions{}

	if err != nil || len(outp) != 1 {
		ret.SendFull = true
	} else {
		token := outp[0][1]
		if token != "-" {
			ret.ResumeToken = token
		}
		snapoutp, snapErr := zfs.ZfsList([]string{"name", "guid"}, "snapshot", destinationDs, "-S", "createtxg")
		if snapErr == nil && len(snapoutp) >= 1 {
			ret.LastSnapshot = zfs.SnapshotName(snapoutp[0][0])
			ret.GUID = snapoutp[0][1]
		}
		// If no snapshots exist and no resume token, ret remains zero-valued
		// (SendFull=false, no token, no last snapshot). The sender will detect
		// this as "no common base" and skip with a warning, asking the operator
		// to remove the destination manually if a full re-send is desired.
	}

	out, err := json.Marshal(ret)
	zfs.FatalIfError(err, "cannot encode IncrementalSuggestions: %w")
	if _, err := os.Stdout.Write(out); err != nil {
		zfs.Fatal("cannot write response to stdout", "err", err)
	}
}

func createNotIfEncrypted(fs string, disableMount bool) error {
	val, err := zfs.ZfsGet(path.Dir(fs), "encryption")
	if err != nil {
		return fmt.Errorf("cannot determine encryption of %s: %w", path.Dir(fs), err)
	}
	if strings.TrimSpace(val) != "off" {
		return fmt.Errorf("filesystem %s is encrypted (encryption=%s): refusing to create an unencrypted child", path.Dir(fs), strings.TrimSpace(val))
	}
	return zfs.ZfsCreate(fs, disableMount)
}

func checkParent(fs string, disableMount bool) error {
	// Base case: fs is a pool name (no "/"). Pools cannot be created with
	// zfs create — they must already exist. Return a clear error rather than
	// recursing into path.Dir(".") == "." which would loop forever.
	if !strings.Contains(fs, "/") {
		if !zfs.Exists(fs) {
			return fmt.Errorf("pool %q does not exist; verify base_dataset configuration", fs)
		}
		return nil
	}
	parent := path.Dir(fs)
	slog.Debug("checking parent exists", "parent", parent)
	if zfs.Exists(parent) {
		return nil
	}
	if err := checkParent(parent, disableMount); err != nil {
		return err
	}
	err := createNotIfEncrypted(parent, disableMount)
	if zfs.Exists(parent) {
		return nil // handles race: another concurrent receive may have created it
	}
	return err
}

func receive(baseds, ds, compression string, cfg *config.ReceiverConfig, fast bool) {
	eg, ctx := zfs.NewGroup(context.Background())

	destinationDs := fmt.Sprintf("%s/%s", baseds, ds)
	// -u suppresses the immediate post-receive mount; -o canmount=off makes the
	// received dataset persistently non-mountable so it does not show up on
	// future 'zfs mount -a' or boot. Together they fulfil the disable_mount
	// contract: the received dataset is never mounted under any circumstance.
	disableMount := cfg.DisableMount == nil || *cfg.DisableMount
	recvArgs := []string{"receive", "-vu"}
	if disableMount {
		recvArgs = append(recvArgs, "-o", "canmount=off")
	}
	for _, p := range cfg.EnforceLocalProperties {
		if p == "" || strings.ContainsAny(p, " \t\n\r") {
			zfs.Fatal("enforce_local_properties entry is empty or contains whitespace", "property", p)
		}
		if strings.HasPrefix(p, "-") {
			zfs.Fatal("enforce_local_properties entry must not start with '-' (would be misinterpreted as a zfs receive flag)",
				"property", p)
		}
		// zfs receive rejects '-o canmount=off' and '-x canmount' together;
		// the explicit -o we just set is the stronger guarantee, so drop the -x.
		if disableMount && p == "canmount" {
			slog.Debug("ignoring enforce_local_properties=canmount; superseded by disable_mount=true (-o canmount=off)")
			continue
		}
		recvArgs = append(recvArgs, "-x", p)
	}
	resumable := cfg.Resumable == nil || *cfg.Resumable
	if resumable {
		recvArgs = append(recvArgs, "-s")
	}
	for _, x := range cfg.ForceOverwriteDatasets {
		if x == destinationDs {
			slog.Info("zfs receive -F applied; remove this entry from force_overwrite_datasets after recovery to prevent future overwrites",
				"dataset", destinationDs)
			recvArgs = append(recvArgs, "-F")
			break
		}
	}
	if !fast {
		zfs.FatalIfError(checkParent(destinationDs, disableMount), "checking parent exists: %w")
	}
	recvArgs = append(recvArgs, destinationDs)

	bufferedData, bufferCmd := zfs.MaybeMbuffer(ctx, cfg.MbufferArgs, os.Stdin)
	uncompressedData, decomprCmd := zfs.MaybeDecompress(ctx, compression, bufferedData)

	cmd := zfs.DefaultExecCommand(ctx, "zfs", recvArgs...)
	cmd.Stdin = uncompressedData
	cmd.Stdout = os.Stdout
	if err := cmd.Start(); err != nil {
		zfs.Fatal("cannot start zfs receive", "err", err, "cmd", cmd.Args)
	}

	eg.Go(func() error {
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("zfs receive failed: %w", err)
		}
		return nil
	})
	if decomprCmd != nil {
		eg.Go(func() error {
			if err := decomprCmd.Wait(); err != nil {
				return fmt.Errorf("decompressor %v failed: %w", decomprCmd.Args, err)
			}
			return nil
		})
	}
	if bufferCmd != nil {
		eg.Go(func() error {
			if err := bufferCmd.Wait(); err != nil {
				return fmt.Errorf("mbuffer %v failed: %w", bufferCmd.Args, err)
			}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		zfs.Fatal("receive pipeline failed", "err", err)
	}
}

func Main() {
	receiverFlags := flag.NewFlagSet("receiver", flag.ExitOnError)
	baseDataset := receiverFlags.String("base_dataset", "", "root destination dataset; overrides config")
	debug := receiverFlags.Bool("debug", false, "enable debug logging")
	configFile := receiverFlags.String("config", "", "path to receiver config file")
	receiverFlags.Parse(os.Args[2:])
	zfs.SetupLogger(*debug)

	// Config is optional; receiver is invoked as an SSH ForceCommand and is
	// never run concurrently against the same config, so no flock is needed.
	cfg := &config.ReceiverConfig{}
	if *configFile != "" {
		f, err := os.Open(*configFile)
		zfs.FatalIfError(err, "cannot open config file: %w")
		defer f.Close()
		d := json.NewDecoder(f)
		d.DisallowUnknownFields()
		zfs.FatalIfError(d.Decode(cfg), "cannot parse config: %w")
	}

	baseDS := cfg.BaseDataset
	if *baseDataset != "" {
		baseDS = *baseDataset
	}
	if cfg.Resumable == nil {
		t := true
		cfg.Resumable = &t
	}
	if cfg.DisableMount == nil {
		t := true
		cfg.DisableMount = &t
	}
	zfs.FatalIfError(zfs.IsValidZFSDataset(baseDS), "invalid base dataset: %w")

	if len(cfg.MbufferArgs) == 0 && zfs.MbufferPresent() {
		cfg.MbufferArgs = append(cfg.MbufferArgs, "-P 90", "-m 3%")
	}

	clientFlags := flag.NewFlagSet("receiver-client", flag.ExitOnError)
	dataset := clientFlags.String("dataset", "", "destination dataset")
	fast := clientFlags.Bool("fast", false, "skip some existence checks for speed")
	op := clientFlags.String("op", "", "operation: incremental_suggestions | set_placeholders | receive")
	compression := clientFlags.String("compression", "none", "compression algorithm in use")
	if sshCmd := os.Getenv("SSH_ORIGINAL_COMMAND"); sshCmd != "" {
		clientFlags.Parse(strings.Fields(sshCmd))
	}
	clientFlags.Parse(receiverFlags.Args())

	if !*fast && !zfs.Exists(baseDS) {
		zfs.Fatal("base dataset does not exist", "base_dataset", baseDS)
	}
	zfs.FatalIfError(zfs.IsValidZFSDataset(*dataset), "invalid dataset: %w")

	switch strings.ToLower(*op) {
	case "incremental_suggestions":
		incrementalSuggestion(baseDS, *dataset)
	case "set_placeholders":
		setPlaceholders(baseDS, *dataset)
	case "receive":
		receive(baseDS, *dataset, *compression, cfg, *fast)
	default:
		zfs.Fatal("unknown operation", "op", *op)
	}
}
