# AI Agent Context Guide — zfsbackup

## Project Overview

**zfsbackup** is a Go tool for automated, incremental ZFS backups over SSH. It provides encrypted raw send (`zfs send -ecw`), resumable transfers, placeholder bookmarks (so source snapshots can be pruned while incremental chains remain intact), configurable snapshot retention, and Prometheus monitoring.

The project is a single compiled binary (`zfsbackup`) that dispatches to one of six subcommands. Each subcommand reads a JSON config file, performs its work, and exits. There is no daemon, no persistent state beyond ZFS bookmarks, and no inter-module communication at runtime — the sender shells out to the receiver over SSH.

## Repository Layout

```
cmd/zfsbackup/          # main() — flag dispatch to subcommand Main() or runMain()
  main.go               # dispatch
  run.go                # "run" subcommand: snapshot → deleter → sender → monitor
internal/
  config/               # JSON config structs (config.go) and wire protocol (wire.go)
  deleter/              # Prunes snapshots by retention rules
  monitor/              # Exports snapshot freshness as Prometheus metrics
  receiver/             # Accepts ZFS streams from the sender
  sender/               # Sends incremental snapshot streams to receiver
  snapshot/             # Creates ZFS snapshots
  zfs/                  # Shared: exec helpers, ZFS wrappers, ParseDuration, LoadConfig
tests/                  # bats-core integration tests
examples/
  mypool.json           # Unified config example (all modules)
  receiver.json         # Standalone receiver config (deployed on backup host)
Makefile
```

## Technology Stack

- **Go 1.27** — no CGO, statically linked binary, zero external dependencies
- **encoding/json/v2** (stdlib) — config files and internal IPC (sender↔receiver) use JSON; `zfs list -j`/`zpool list -j` output is parsed via `encoding/json/jsontext.Value`. Do not import the v1 `encoding/json` package.
- **log/slog** (stdlib) — structured logging to stderr via `slog.NewTextHandler`; level and source file/line controlled by `SetupLogger(debug)`
- **ZFS** — invoked as external processes (`zfs`, `zpool`, `mbuffer`, `zstd`); binaries are located once at startup and cached

## Config System

All modules use `--config` pointing to a JSON file. The top-level struct is `config.Config`:

```json
{
  "include": ["mypool"],
  "exclude": ["mypool/scratch"],
  "snapshot": { "name_pattern": "snap-2006-01-02_15-04-05" },
  "sender": {
    "snapshot_re": "snap-....-..-.._..-..-..",
    "destinations": [
      { "receiver": "ssh backup@primary -- ",  "placeholders": ["primary"]  },
      { "receiver": "ssh backup@offsite -- ",  "placeholders": ["offsite"], "raw_send": true }
    ]
  },
  "deleter":  { "regex": ["snap-.*"], "rules": [{"interval":"1h","count":48}] },
  "monitor":  { "prometheus_output": "/var/lib/node_exporter/textfile_collector/zfsbackup.prom" }
}
```

Modules with no section in the config are skipped by `zfsbackup run`. Individual subcommands (`snapshot`, `sender`, etc.) require their respective section or they fatal.

The top-level `include`/`exclude` fields are defaults; each module section may provide its own `include`/`exclude` to override them. Use `cfg.ResolveInclude(mc.Include)` and `cfg.ResolveExclude(mc.Exclude)` in each module.

The receiver uses a separate JSON file (`config.ReceiverConfig`), not `config.Config`, because it is deployed on a different machine.

**Never hand-edit generated files.** There are no generated files — all config types are plain Go structs in `internal/config/config.go` and `internal/config/wire.go`.

### Config Loading Pattern

All modules that load a config call `zfs.LoadConfig(path, v)`, which opens the file, acquires an exclusive `flock`, and JSON-decodes it with `zfs.DecodeConfig`. The caller retains the returned `*os.File` and defers its `Close()` until the job completes. This serializes invocations using the same config inode and catches config typos. An atomically replaced config or a different config file has a separate lock. The Receiver is the exception — its config is optional (`if *configFile != ""`), read-only, and shared by concurrent SSH receivers. It opens its config without locking and decodes it with the same `zfs.DecodeConfig`.

`zfs.DecodeConfig(r, v)` is `json.UnmarshalRead(r, v, json.RejectUnknownMembers(true))`. Together with the `encoding/json/v2` defaults it rejects unknown members, member names that differ in case from the struct tag, duplicate member names, invalid UTF-8, and trailing data after the single top-level value. `RejectUnknownMembers` is **not** a v2 default: always decode configs through `DecodeConfig`, never with a bare `json.Unmarshal`/`json.UnmarshalRead`.

### Duration Strings

The `zfs.ParseDuration` function parses durations with suffixes: `s`, `m`, `h`, `d`, `w`, `y`. Standard Go `time.Duration` strings are **not** used in config files.

## Module Details

### Run (`zfsbackup run`)

**Config**: top-level `Config` struct
**Flags**: `--config`, `--parallelism`, `--dry-run`, `--debug`

Runs snapshot → deleter → sender → monitor in sequence. Modules without a config section are skipped. All modules run regardless of individual failures; errors are collected via `errors.Join` and reported at the end.

### Snapshot

**Config field**: `cfg.Snapshot` (`*config.SnapshotConfig`)
**Typical schedule**: every 30 minutes via systemd timer
**Exported function**: `snapshot.Run(cfg *config.Config, dryRun bool) error`

Creates atomic snapshots of all included filesystems within each pool in a single `zfs snapshot` call (to ensure consistent TXG across datasets). The `skip_empty_younger_than` option skips filesystems whose last snapshot is recent and `written=0`.

`name_pattern` uses Go's reference time (`Mon Jan 2 15:04:05 MST 2006`). Example: `"snap-2006-01-02_15-04-05"`.

### Deleter

**Config field**: `cfg.Deleter` (`*config.DeleterConfig`)
**Typical schedule**: hourly
**Exported function**: `deleter.Run(cfg *config.Config, parallelism int, dryRun bool) error`

Applies a single retention policy to all filesystems resolved from the config's include/exclude. Rules are interval-based: for each `RetentionRule`, the most recent snapshot in each time bucket is kept. The reference point is the timestamp of the **most recent snapshot** (not wall clock).

`preserve_top_n` and `preserve_newer_than` are independent of interval rules and always apply first. `preserve_newer_than` uses the same frozen reference time as interval rules (the most recent snapshot's timestamp), **not wall-clock time**.

`allow_holes: true` tolerates gaps in interval coverage. Without it, a gap triggers an error.

`--dry-run` defaults to `true`. Pass `--dry-run=false` to actually delete.

### Sender

**Config field**: `cfg.Sender` (`*config.SenderConfig`)
**Typical schedule**: hourly
**Exported function**: `sender.Run(cfg *config.Config, parallelism int, limitFs string) error`

For each included filesystem, the sender:
1. Calls the receiver with `--op=incremental_suggestions` to learn the current state of the destination (last snapshot GUID, resume token, or "send full").
2. Selects snapshots to send: by default, only the most recent matching snapshot; with `send_intermediate: true`, all snapshots in order.
3. Before sending: creates `#<snap>-before-<placeholder>` bookmarks for `placeholders`.
4. Pipes `zfs send` output through optional mbuffer and zstd compression to the receiver.
5. After sending: creates/updates `#<snap>-<placeholder>` bookmarks, garbage-collects stale ones.
6. Optionally syncs placeholder bookmarks to the receiver (`sync_placeholders`).

**Placeholder bookmarks** (`placeholders`) allow the source snapshot to be pruned by the deleter while the sender still has a valid incremental base (the bookmark). Without them, you cannot prune source snapshots without breaking the incremental chain.

`destinations` is the list of backup targets. Each entry is processed independently per filesystem, so a single `zfsbackup sender` run can fan out to multiple receivers. Per-destination fields: `receiver`, `compression`, `compression_level`, `raw_send`, `placeholders`, `sync_placeholders`, `mbuffer_args`.

`receiver` (inside each destination) is the command for invoking that receiver. For SSH: `"ssh user@host -- "`. For local: `"zfsbackup receiver --config /etc/zfsbackup/receiver.json -- "`. Split on whitespace and appended directly (no shell).

`snapshot_re` (per-job) is a regex matched against the snapshot name (without the dataset prefix). Strongly recommended to avoid sending unintended snapshots.

### Receiver

**Config type**: `config.ReceiverConfig` (separate JSON file, optional)
**Deployment**: SSH `ForceCommand` in `authorized_keys`

The receiver is not scheduled — it is invoked on demand by the sender over SSH. It accepts three operations:
- `--op=incremental_suggestions` — inspects the destination dataset, returns what to send next (JSON to stdout)
- `--op=set_placeholders` — reads a `SetPlaceholders` JSON from stdin, creates bookmarks on the destination
- `--op=receive` — pipes stdin into `zfs receive`

`--base_dataset` (flag or config) sets the root under which received datasets are stored. `--base_dataset=tank/backups/myhost` + `--dataset=mypool/data` → destination is `tank/backups/myhost/mypool/data`.

`disable_mount: true` (the default) creates received datasets with `canmount=off` — they are not mounted automatically.

`resumable: true` (the default) enables `zfs receive -s`. If interrupted, the next send can resume instead of restarting.

### Monitor

**Config field**: `cfg.Monitor` (`*config.MonitorConfig`)
**Typical schedule**: every 4 hours
**Exported function**: `monitor.Run(cfg *config.Config) error`

Writes Prometheus text-format metrics to `prometheus_output` and also prints them to stdout. Exports:
- `LastSnapAge{fs=...}` — seconds since the most recent snapshot
- `LastSnapTimestamp{fs=...}` — Unix timestamp of the most recent snapshot
- `PoolUsedSpacePercent{pool=...}` — pool capacity percentage
- `HasBrokenPool` — count of pools not in ONLINE state

## Wire Protocol (sender ↔ receiver)

The sender and receiver communicate via JSON on the SSH channel's stdin/stdout. Types are defined in `internal/config/wire.go`:

- **`IncrementalSuggestions`** — JSON written by the receiver to stdout for `incremental_suggestions` requests
- **`SetPlaceholders`** — JSON written by the sender to the receiver's stdin for `set_placeholders` requests
- **`PlaceholderEntry`** — entry in `SetPlaceholders.Placeholders`

Both sides use `encoding/json/v2` with default options, so unknown wire members are ignored (forward compatibility). The receiver's `incremental_suggestions` handler writes with `json.MarshalWrite(os.Stdout, …)`; the sender decodes with `json.Unmarshal`. The `set_placeholders` flow is reversed: the sender marshals and writes to stdin, the receiver decodes with `json.UnmarshalRead(os.Stdin, …)`.

## Shared `zfs` Package

All modules import `internal/zfs`. Key exports:

| Symbol | Purpose |
|---|---|
| `LoadConfig(path, v any)` | Open, flock, `DecodeConfig`; caller closes the returned file after the job |
| `DecodeConfig(r, v any)` | Strict single-value JSON decode for config files (rejects unknown/duplicate members) |
| `FatalIfError(err, format)` | Log fatal + exit if err != nil; format must contain `%w` |
| `MyFatalFn(v)` / `MyFatalFnF(fmt, args)` | Log fatal message + exit |
| `SetupLogger(debug bool)` | Configure slog text handler on stderr with source annotation |
| `Fatal(msg, args...)` | Log at Error level and call os.Exit(1) |
| `DefaultExecCommand(ctx, arg0, args...)` | Exec wrapper: resolves binary path, sets `LC_ALL=C`, forwards stderr |
| `ParseDuration(s)` | Parse `"30m"`, `"7d"`, `"1w"`, etc. |
| `ZfsList(props, type, dataset, flags...)` | Run `zfs list -j -p`, parse JSON, sort, return rows |
| `ZpoolList(props)` | Run `zpool list -j -p`, parse JSON, return rows sorted by pool name |
| `ExpandFsToProcess(include, exclude)` | Return sorted discovered filesystems and joined discovery errors; validate all filters first |
| `IsValidZFSDataset(name)` | Validate a dataset name (no path traversal, no special chars) |
| `MaybeMbuffer(ctx, args, reader)` | Wrap reader with mbuffer if args non-empty |
| `MaybeCompress(ctx, type, level *int, reader)` | Wrap reader with zstd compressor |
| `MaybeDecompress(ctx, format, reader)` | Wrap reader with decompressor |
| `ZfsSetPlaceholder / ZfsSetBeforeBookmark` | Create bookmark + GC stale placeholders |

## Error Handling Conventions

- **Fatal errors** (config parse failures, ZFS unavailable, invalid dataset names): call `FatalIfError` or `MyFatalFn` — these log and call `os.Exit(1)`.
- **Per-filesystem errors** (one dataset fails, others should continue): collected in a `[]error` slice, joined with `errors.Join`, returned from `Run()`, fataled in `Main()`.
- **Pipeline errors**: returned from `eg.Wait()` and propagated up.

The `Run()` functions return errors rather than calling Fatal directly for per-FS errors. The Fatal calls remain in `Main()` for startup failures. `runMain()` collects `Run()` errors and fatals at the end.

## Logging

All logging goes through `log/slog` (stdlib) to stderr. Use the package-level functions:
```go
slog.Debug("message", "key", value)
slog.Info("message", "fs", fsp.fs)
slog.Warn("message", "err", err)
slog.Error("message", "err", err)
zfs.Fatal("message", "err", err)  // logs at Error, then os.Exit(1)
```

`SetupLogger(debug)` must be called before any logging. Modules call it immediately after flag parsing. It installs a `slog.NewTextHandler` on stderr with `AddSource: true`.

## Import Grouping

Go files use two or three import groups, separated by blank lines:
1. Standard library
2. Internal packages (`github.com/mikispag/zfsbackup/internal/...`)

(No external packages remain — the module has zero external dependencies.)

`gofmt` and `goimports` enforce alphabetical order within each group.

## Building

```sh
make build        # produces ./zfsbackup
make deb          # Debian package via fpm; GPG-signs if SIGN=1 (default)
make pacman       # Arch Linux package via fpm
make SIGN=0 deb   # Skip signing
```

The binary is statically linked (`CGO_ENABLED=0`). All runtime dependencies (`zfs`, `zpool`, `mbuffer`, `zstd`) are resolved via PATH at startup.

## Releasing

Releases are cut on demand from `main` with the Release workflow (`.github/workflows/release.yml`):

```sh
gh workflow run release.yml -f version=v1.2.3                  # publish
gh workflow run release.yml -f version=v1.3.0-rc.1 -f prerelease=true
gh workflow run release.yml -f version=v1.2.3 -f dry_run=true  # build and preview notes only
```

The workflow validates the version (semver with a `v` prefix, tag must not exist), runs the full CI workflow through `workflow_call`, builds reproducible `tar.gz` archives for linux and freebsd on amd64 and arm64 with `-trimpath` and `main.version` set, writes `SHA256SUMS`, attests build provenance, and creates the tag and GitHub release in a separate job that is the only one with `contents: write`. A dry run stops before attesting and publishing and shows the notes in the job summary.

Release notes come from `.github/scripts/release-notes.sh`, which groups commit subjects since the previous tag by Conventional Commits type (stable releases skip pre-release tags; `!` or `BREAKING CHANGE` produce a breaking-changes section). Keep commit subjects in that form: anything else lands under "Other changes". The workflow references CI with GitHub's self-repository syntax (`uses: $/...`); actionlint v1.7.12 does not know that syntax yet and reports a false positive.

## Testing

```sh
make unit-tests         # go vet ./... && go test ./...
make integration-tests  # bats tests/tests.bats (requires a delegated ZFS filesystem)
make tests              # both
```

CI (`.github/workflows/ci.yml`) additionally gates on `go mod tidy -diff`, staticcheck and govulncheck at pinned versions, and runs the bats suite against OpenZFS 2.4.4 built from a checksum-verified tarball. Before pushing, run the same pinned tools locally, e.g. `go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...` and `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...`. When bumping OpenZFS, update `ZFS_VERSION` and `ZFS_SHA256` together.

**Unit tests** cover:

| Package | What is tested |
|---|---|
| `internal/config` | `ResolveInclude`/`ResolveExclude`; full JSON round-trip of all wire types (`IncrementalSuggestions`, `SetPlaceholders`, `PlaceholderEntry`) including `omitempty` and exact field name checks |
| `internal/zfs` | `ParseDuration` (valid, invalid, ParseInt overflow); `IsValidZFSDataset` (valid names, invalid names, `.`/`..` components); `ParseTabular` (success, empty output, command fails); `Group`/`NewGroup`/`Go`/`Wait` (success, error propagation, context cancellation, first-error-wins) |
| `internal/sender` | `autoPlaceholderName` (stability, whitespace normalization, no hyphens, distinct names); `compressionType` (empty→"none", explicit value); `PotentialSnapsToSend`; `ActualSnapsToSend` (no base, intermediate, placeholder-driven mandatory, bookmark base); `baseFor` (snapshot preferred over a bookmark with the same GUID); `incrementalArgs` |
| `internal/deleter` | `preserveTopN`; `preserveNewerThan`; `markForPreservation` (interval rules, holes, invalid duration, snap outside window, large-interval bias capping, invalid `preserve_newer_than` path); `newDeleteFsProcessor` (valid/invalid regexp); `deleteManySnaps` (nil/empty list) |
| `internal/monitor` | `metric.asPrometheus` (no dims, one dim, multiple dims, negative value); `mon.asPrometheus` (HELP/TYPE headers, dedup, ordering) |

Unit tests create `deleteFsProcessor` and `fsProcessor` structs directly — they do **not** call `newDeleteFsProcessor` or `NewFSProcessor`. Tests for `markForPreservation` bypass ZFS entirely by populating `dfp.snaps` directly.

**Integration tests** require a delegated ZFS filesystem (`DELEGATED_FS=pool/testfs`). They exercise the full pipeline including actual `zfs` commands.

## Critical Rules

1. **Dataset name validation**: always call `zfs.IsValidZFSDataset` before using a name in a ZFS command. The regex `^[A-Z0-9a-z_.:-]+(/[A-Z0-9a-z_.:-]+)*$` is the gate; dataset names cannot contain `@`, `#`, spaces, or path separators.

2. **Sender and Receiver are a matched pair**: they communicate via JSON on stdin/stdout. The sender's `receiver` must invoke the same version of the receiver binary. Protocol changes require updating both sides.

3. **Never break the incremental chain**: if you remove placeholder bookmark logic or change the GUID-matching in `baseFor`/`ActualSnapsToSend`, the sender will fall back to full sends, which can be extremely expensive. Verify with the existing `ActualSnapsToSend` tests before changing selection logic.

4. **Config files are flocked**: keep the `*os.File` returned by `LoadConfig` open until the entire job completes. This serializes jobs using the same config inode. Different config files or atomic file replacement do not share that lock. The read-only receiver config remains exempt.

5. **JSON format, not prototext**: config files are parsed with `encoding/json/v2` via `zfs.DecodeConfig`. Field names are JSON snake_case as defined in the struct tags and are matched case-sensitively. Unknown or duplicate members cause a parse error.

6. **`dfp.now` is set to the most recent snapshot's timestamp, not `time.Now()`**: retention rules are evaluated relative to the last snapshot, not the current wall clock. This is intentional — if snapshotting stops, the retention window freezes rather than expiring all old snapshots.

7. **`zfs send` for large initial transfers can run for hours**: the sender's systemd service uses `TimeoutStartSec=0` for this reason. Do not add timeouts to `zfs send` invocations.

## Common Pitfalls

**Pitfall: breaking the JSON config format**
When adding fields to a config struct, old config files that don't mention the new field still parse successfully (zero value). Fields that should default to true (currently `include_properties`, `resumable`, and `disable_mount`) are set to true in `Main()` or `Run()` after config load using nil-check patterns (`if cfg.Resumable == nil { t := true; cfg.Resumable = &t }`).

**Pitfall: `omitempty` on `bool` or numeric fields**
In `encoding/json/v2`, `omitempty` omits a field only when its *JSON* encoding is empty (`null`, `""`, `{}`, `[]`), so `false` and `0` are still emitted. Use `omitzero` for `bool` and integer fields (as on `send_full`, `force_overwrite`, `raw_send`, `preserve_top_n`); keep `omitempty` for strings, slices, maps and pointers. `internal/config/wire_test.go` asserts that a zero `IncrementalSuggestions` marshals to `{}`.

**Pitfall: shadowing `path` (the package) with a variable named `path`**
`internal/receiver/receiver.go` imports the `path` package. Use `filePath` or `dirPath` for local variables.

**Pitfall: `zfs.FatalIfError` does not return**
`FatalIfError` calls `os.Exit(1)` when err != nil. Code after `FatalIfError` is unreachable on error. Do not assign return values after a `FatalIfError` call on the assumption the error was nil — that invariant holds, but the pattern can confuse readers.

**Pitfall: forgetting that `zfs.Fatal()` skips defers**
`zfs.Fatal()` calls `os.Exit(1)`, which does not run deferred functions. Files opened before a fatal call are closed by the OS, but cleanup logic in defers will not run.

**Pitfall: adding regex compilation inside a per-snapshot loop**
`getValidatedSnaps` in the deleter is called for every filesystem on every run and iterates all snapshots. Compiling regexps inside this loop is O(snapshots × patterns). Regexps are compiled once in `newDeleteFsProcessor` and stored in `dfp.regexps`.

**Pitfall: Receiver config is optional but other modules' configs are required**
All modules except Receiver check for their section in the config and call Fatal if absent. The Receiver guards its config load with `if *configFile != ""` because it can run with defaults (when `--base_dataset` is passed directly).

**Pitfall: placeholder suffix must not contain hyphens**
`zfsGcPlaceholder` in `internal/zfs/zfs.go` splits bookmark names on `-` and takes the last component as the placeholder suffix. Auto-derived placeholder names use the format `dst` + 16 hex chars (FNV-64a hash of the normalized receiver command) — no hyphens. Explicit `placeholders` entries must also be hyphen-free. With multiple destinations, each must have a distinct suffix to avoid bookmark collisions.

**Pitfall: per-destination fields vs per-job fields in SenderConfig**
`receiver`, `compression`, `compression_level`, `raw_send`, `placeholders`, `sync_placeholders`, and `mbuffer_args` are per-destination and live in `config.DestinationConfig`. `snapshot_re`, `send_intermediate`, and `include_properties` are per-job and live in `config.SenderConfig`; its `resumable` key is deprecated and ignored (still parsed so old configs load, with a warning). Resumability is decided by the receiver's `resumable`; the sender always resumes when the receiver reports a token. The receiver still accepts the obsolete `--fast` client flag as a no-op for older senders. `fsProcessor` carries both as `job *config.SenderConfig` and `dst *config.DestinationConfig`.
