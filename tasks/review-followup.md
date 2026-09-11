# Follow-up design and code review — 2026-09-11

Reviewed commit `182e354` across all application packages, shared helpers,
configuration, CLI behavior, integration tests, documentation and packaging.
The earlier review remains in `tasks/review.md`. This pass addresses additional
findings and preserves the stateless design, wire fields and per-pool atomic
snapshot operations.

## Findings and resolutions

| Priority | Finding | Resolution |
| --- | --- | --- |
| High | Receiver-controlled resume tokens could select a different dataset or an excluded snapshot. | Resolve the token using a ZFS dry run and validate the actual target against eligible local snapshots before transmitting. Reject missing or ambiguous targets. |
| High | Resumed sends did not enforce the destination's raw-encryption requirement. | Pass `-w` during both resume inspection and transfer for raw destinations. Add a real encrypted-resume test with the source key unloaded. |
| High | Successful resumes left checkpoint promotion unfinished. | Create the before-send bookmark for the validated target and promote the completed checkpoint after successful resumption. |
| High | The sender skipped mandatory placeholder snapshots outside `snapshot_re`. | Check the complete selection policy before deciding that only bookmark synchronization is needed. |
| High | Config locks were released after parsing, allowing same-config jobs to race snapshot, deletion and bookmark operations. | Retain the locked file for the complete CLI invocation and close it on completion. Test lock exclusion and release. The lock belongs to the open inode; other files and atomic replacement are separate locks. |
| High | Filesystem discovery called `os.Exit`, preventing later modules and monitoring from running. | Return discovered filesystems plus joined errors. Process valid roots, collect failures and continue the unified run. Validate all include/exclude filters before discovery. |
| High | Dataset names such as `-r` passed validation and could be interpreted as ZFS options. | Reject option-like dataset operands, validate shared wrapper inputs, and separately validate snapshot/bookmark components to preserve safe names beginning with a hyphen. |
| Medium | Incomplete ZFS JSON was treated as an empty inventory or as blank properties. | Require the inventory object and requested properties for both filesystem and pool listings. |
| Medium | Invalid retention settings, regexes and skip durations were accepted when there was nothing to process. | Validate before discovery; reject nonpositive deleter parallelism; compile retention regexes once and share them read-only across filesystem workers. |
| Medium | Filesystems with no snapshots had no freshness metrics. | Emit `LastSnapTimestamp=0` and age measured from the Unix epoch, allowing freshness alerts to identify missing history. |
| Medium | Partial collection failures could leave apparently healthy Prometheus output. | Publish available samples and `MonitorSuccess=0` on collection failure; emit `1` only after complete successful collection. |
| Medium | A changed `VERSION` could leave the existing binary's embedded version stale. | Rebuild the binary for each requested build and verify explicit/default version output. |
| Medium | Packages omitted or underspecified their mandatory ZFS dependency. | Require `zfsutils-linux >= 2.3` for Debian and `zfs-utils >= 2.3` for Arch, matching the JSON command interface used by the application. |
| Medium | Git-derived versions could produce invalid package versions or mismatched signing filenames. | Normalize tag/hash/dirty versions once and use the resulting package version for both FPM and signing paths. |
| Documentation | The context guide described Go 1.22, obsolete signatures and parse-only locking. | Update it to Go 1.25 and the implemented discovery/locking APIs; document new monitoring and packaging behavior in the README. |

## Verification

- Regression tests demonstrated rejected resume targets, raw-flag omissions,
  missing resume checkpoints, skipped mandatory snapshots, early lock release,
  unsafe dataset operands, incomplete JSON and invalid empty-job configuration.
- CLI tests cover continuing to monitoring after discovery failure and publishing
  failure status alongside valid samples.
- Repository checks: `make unit-tests`, `go test -race -count=1 ./...`,
  `make build`, and `git diff --check`.
- Build-version overrides were checked against the produced binary. Package
  recipes were checked for tags, git-describe versions, hashes and development
  versions. FPM is unavailable locally, so actual package creation and signing
  remain unverified.
- The Bats suite now contains 25 integration cases, including raw resumption with
  an unloaded source key. Real ZFS execution runs in GitHub Actions after push;
  this user has no locally delegated test dataset.

## Source checks

Resume behavior and parsable dry-run output were checked against
[OpenZFS 2.4.2 send/receive implementation](https://github.com/openzfs/zfs/blob/zfs-2.4.2/lib/libzfs/libzfs_sendrecv.c)
and [CLI flag handling](https://github.com/openzfs/zfs/blob/zfs-2.4.2/cmd/zfs/zfs_main.c).
Package-version behavior was checked against
[FPM's Debian package implementation](https://github.com/jordansissel/fpm/blob/main/lib/fpm/package/deb.rb).
