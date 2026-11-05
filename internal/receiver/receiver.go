package receiver

import (
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/mikispag/zfsbackup/internal/config"
	"github.com/mikispag/zfsbackup/internal/zfs"
)

func setPlaceholders(baseds, ds string) error {
	sp := &config.SetPlaceholders{}
	if err := json.UnmarshalRead(os.Stdin, sp); err != nil {
		return fmt.Errorf("cannot decode SetPlaceholders: %w", err)
	}

	interestingGuids := make(map[string]bool, len(sp.Placeholders))
	for _, p := range sp.Placeholders {
		if !zfs.IsValidPlaceholder(p.Name) {
			return fmt.Errorf("invalid placeholder suffix %q", p.Name)
		}
		interestingGuids[p.GUID] = true
	}
	if err := zfs.IsValidZFSDataset(sp.FS); err != nil {
		return fmt.Errorf("invalid filesystem in SetPlaceholders payload: %w", err)
	}
	if sp.FS != ds {
		return fmt.Errorf("filesystem disagreement between payload (%s) and argument (%s)", sp.FS, ds)
	}

	destinationDs := fmt.Sprintf("%s/%s", baseds, sp.FS)
	snaps, err := zfs.ZfsList([]string{"name", "guid"}, "snapshot", destinationDs)
	if err != nil {
		return fmt.Errorf("cannot list snapshots: %w", err)
	}
	bms, err := zfs.ZfsList([]string{"name", "guid"}, "bookmark", destinationDs)
	if err != nil {
		return fmt.Errorf("cannot list bookmarks: %w", err)
	}

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

	var errs []error
PLACEHOLDERS:
	for _, p := range sp.Placeholders {
		guid := p.GUID
		if len(byGuidBms[guid]) == 0 && len(byGuidSnaps[guid]) == 0 {
			errs = append(errs, fmt.Errorf("no snapshot/bookmark for %v", p))
			continue
		}
		for _, b := range byGuidBms[guid] {
			if strings.HasSuffix(b, "-"+p.Name) {
				continue PLACEHOLDERS
			}
		}
		items := append(byGuidSnaps[guid], byGuidBms[guid]...)
		if err := zfs.ZfsSetPlaceholder(items[0], p.Name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func incrementalSuggestion(baseds, ds string, cfg *config.ReceiverConfig) error {
	destinationDs := fmt.Sprintf("%s/%s", baseds, ds)
	outp, err := zfs.ZfsList([]string{"name", "receive_resume_token"}, "filesystem", destinationDs)
	ret := &config.IncrementalSuggestions{}

	if err != nil || len(outp) != 1 {
		// A failed property query can also indicate permissions or a pool
		// failure. Confirm absence before requesting an expensive full send.
		filesystems, listErr := zfs.ZfsList([]string{"name"}, "filesystem", baseds, "-r")
		if listErr != nil {
			return fmt.Errorf("cannot inspect destination datasets: %w", listErr)
		}
		for _, row := range filesystems {
			if row[0] == destinationDs {
				return fmt.Errorf("cannot inspect existing destination %s: %v", destinationDs, err)
			}
		}
		ret.SendFull = true
	} else {
		token := outp[0][1]
		if token != "-" {
			ret.ResumeToken = token
		}
		snapoutp, err := zfs.ZfsList([]string{"name", "guid"}, "snapshot", destinationDs, "-S", "createtxg")
		if err != nil {
			return fmt.Errorf("cannot list destination snapshots: %w", err)
		}
		if len(snapoutp) >= 1 {
			ret.LastSnapshot = zfs.SnapshotName(snapoutp[0][0])
			ret.GUID = snapoutp[0][1]
		}
		// If no snapshots exist and no resume token, ret remains zero-valued
		// (SendFull=false, no token, no last snapshot) and the sender skips —
		// unless ForceOverwrite below opts this destination in to a full
		// re-send under zfs receive -F.
	}

	for _, x := range cfg.ForceOverwriteDatasets {
		if x == destinationDs {
			ret.ForceOverwrite = true
			break
		}
	}

	if err := json.MarshalWrite(os.Stdout, ret); err != nil {
		return fmt.Errorf("cannot write IncrementalSuggestions: %w", err)
	}
	return nil
}

func createNotIfEncrypted(fs string, disableMount bool) error {
	encrypted, err := zfs.IsEncrypted(path.Dir(fs))
	if err != nil {
		return err
	}
	if encrypted {
		return fmt.Errorf("filesystem %s is encrypted: refusing to create an unencrypted child", path.Dir(fs))
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

func receive(baseds, ds, compression string, cfg *config.ReceiverConfig) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eg, ctx := zfs.NewGroup(ctx)

	destinationDs := fmt.Sprintf("%s/%s", baseds, ds)
	// -u suppresses the immediate post-receive mount; -o canmount=off makes the
	// received dataset persistently non-mountable so it does not show up on
	// future 'zfs mount -a' or boot. Together they fulfil the disable_mount
	// contract: the received dataset is never mounted under any circumstance.
	disableMount := cfg.DisableMount == nil || *cfg.DisableMount
	recvArgs := []string{"receive", "-v"}
	if disableMount {
		recvArgs = append(recvArgs, "-u", "-o", "canmount=off")
	}
	for _, p := range cfg.EnforceLocalProperties {
		if p == "" || strings.ContainsAny(p, " \t\n\r") {
			return fmt.Errorf("enforce_local_properties entry %q is empty or contains whitespace", p)
		}
		if strings.HasPrefix(p, "-") {
			return fmt.Errorf("enforce_local_properties entry %q must not start with '-' (would be misinterpreted as a zfs receive flag)", p)
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
	if err := checkParent(destinationDs, disableMount); err != nil {
		return fmt.Errorf("checking parent exists: %w", err)
	}
	recvArgs = append(recvArgs, destinationDs)

	started := []*exec.Cmd{}
	defer func() {
		cancel()
		for _, process := range started {
			process.Wait()
		}
	}()
	bufferedData, bufferCmd, err := zfs.MaybeMbuffer(ctx, cfg.MbufferArgs, os.Stdin)
	if err != nil {
		return err
	}
	if bufferCmd != nil {
		started = append(started, bufferCmd)
	}
	uncompressedData, decomprCmd, err := zfs.MaybeDecompress(ctx, compression, bufferedData)
	if err != nil {
		return err
	}
	if decomprCmd != nil {
		started = append(started, decomprCmd)
	}
	cmd := zfs.DefaultExecCommand(ctx, "zfs", recvArgs...)
	cmd.Stdin = uncompressedData
	cmd.Stdout = os.Stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cannot start zfs receive: %w", err)
	}
	started = append(started, cmd)

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
	err = eg.Wait()
	started = nil
	return err
}

func Main() {
	receiverFlags := flag.NewFlagSet("receiver", flag.ExitOnError)
	baseDataset := receiverFlags.String("base_dataset", "", "root destination dataset; overrides config")
	debug := receiverFlags.Bool("debug", false, "enable debug logging")
	configFile := receiverFlags.String("config", "", "path to receiver config file")
	receiverFlags.Parse(os.Args[2:])
	zfs.SetupLogger(*debug)

	// Config is optional and read-only; parallel SSH receivers may share it.
	cfg := &config.ReceiverConfig{}
	if *configFile != "" {
		f, err := os.Open(*configFile)
		zfs.FatalIfError(err, "cannot open config file: %w")
		defer f.Close()
		zfs.FatalIfError(zfs.DecodeConfig(f, cfg), "cannot parse config: %w")
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
	// --fast used to skip existence checks; older senders still pass it.
	clientFlags.Bool("fast", false, "ignored; accepted for compatibility with older senders")
	op := clientFlags.String("op", "", "operation: incremental_suggestions | set_placeholders | receive")
	compression := clientFlags.String("compression", "none", "compression algorithm in use")
	if sshCmd := os.Getenv("SSH_ORIGINAL_COMMAND"); sshCmd != "" {
		clientFlags.Parse(strings.Fields(sshCmd))
		if clientFlags.NArg() != 0 {
			zfs.Fatal("unexpected SSH receiver arguments", "args", clientFlags.Args())
		}
	}
	clientFlags.Parse(receiverFlags.Args())
	if clientFlags.NArg() != 0 {
		zfs.Fatal("unexpected receiver arguments", "args", clientFlags.Args())
	}

	if !zfs.Exists(baseDS) {
		zfs.Fatal("base dataset does not exist", "base_dataset", baseDS)
	}
	zfs.FatalIfError(zfs.IsValidZFSDataset(*dataset), "invalid dataset: %w")

	var err error
	switch strings.ToLower(*op) {
	case "incremental_suggestions":
		err = incrementalSuggestion(baseDS, *dataset, cfg)
	case "set_placeholders":
		err = setPlaceholders(baseDS, *dataset)
	case "receive":
		err = receive(baseDS, *dataset, *compression, cfg)
	default:
		zfs.Fatal("unknown operation", "op", *op)
	}
	if err != nil {
		zfs.Fatal("receiver operation failed", "op", *op, "dataset", *dataset, "err", err)
	}
}
