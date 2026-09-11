# Design and code review — 2026-09-11

Reviewed all Go packages, configuration and wire types, CLI dispatch, integration
fixtures, examples, README, packaging and CI. All confirmed actionable findings
listed below are fixed in this change. The design remains a stateless command-line
tool with independent backup destinations and no additional dependencies.

## Findings and implementations

| Priority | Finding | Resolution and verification |
| --- | --- | --- |
| High | Retention chose the oldest snapshot in a bucket and retained `count + 1` buckets. | Select the newest snapshot in exactly `count` buckets; preserve the latest-snapshot time anchor and independent preservation rules. Regression tests and deletion integration expectations cover the changed selection. |
| High | Zero/negative retention settings could panic; enormous counts could exhaust memory. | Validate settings and track bucket transitions without allocating an array proportional to count. Test invalid and maximum integer counts. |
| High | An unusable incremental base or a disabled resume capability could report a successful backup. | Return actionable errors; full overwrite still requires explicit receiver configuration. Regression tests cover both states. |
| High | Receiver metadata failures were mistaken for absent destinations, triggering full sends. | Confirm absence with a separate listing; propagate property, pool and snapshot listing failures. Test missing destinations separately from failures. |
| High | Existing bookmark names were accepted without checking snapshot identity. | Require matching GUIDs before treating creation failure as an idempotent retry; do not garbage-collect on mismatch. Test matching and conflicting GUIDs. |
| High | `run --dry-run` still invoked the sender. | Skip sending while previewing snapshots and deletion decisions; test that the sender is not invoked. |
| High | Pipeline startup failures could terminate the process or leave helpers running; producers could be waited on before consumers inherited their pipes. | Return helper errors, register cleanup before startup, cancel and reap partially started pipelines, and start all stages before waiting. Test startup errors and invalid compression formats. |
| Medium | Snapshot regex alternatives were not anchored as a complete expression. | Group alternatives before anchoring; test partially matching names. |
| Medium | Placeholder synchronization chose the last lexical name rather than the newest transaction group. | Order by TXG before choosing each suffix's checkpoint; regression test reverses lexical and temporal order. |
| Medium | Bookmark-backed placeholder creation called a snapshot-only name parser. | Accept and validate snapshot or bookmark sources; avoid repeatedly appending the same suffix. Test bookmark sources and invalid input. |
| Medium | Stale placeholders survived while their snapshots existed, contradicting the single-checkpoint contract and integration fixtures. | After replacement succeeds, remove obsolete bookmarks with the owned suffix. Preserve other suffixes and all snapshots; document exclusive suffix ownership across jobs. Regression test fails against the original implementation. |
| Medium | Invalid destination commands, compression types and conflicting/unsafe placeholder suffixes were accepted. | Validate sender configuration before filesystem work; validate receiver payload suffixes and filesystem agreement. |
| Medium | Absolute executable paths failed custom PATH lookup, and missing executables terminated the process. | Use standard executable lookup with the existing system-directory fallback and return startup errors to callers. Test absolute and missing commands. |
| Medium | Duration multiplication could overflow into an incorrect retention window. | Reject negative and out-of-range durations; test overflow boundaries. |
| Medium | Config filenames `help`/`version` triggered global shortcuts; positional arguments silently stopped flag parsing. | Restrict shortcuts to the command position and reject unconsumed arguments, preserving the receiver's local/SSH two-stage parsing. Add CLI regression tests. |
| Medium | Invalid formatted snapshot names reached ZFS. | Validate the name before filesystem expansion or commands; test valid and invalid patterns. |
| Medium | `disable_mount=false` still passed `-u` to the receiver. | Apply both `-u` and `canmount=off` only when mounting is disabled; test receive arguments for default and explicit settings. |
| Medium | Config decoders accepted trailing JSON values or garbage. | Require exactly one JSON value in unified and receiver configs; test both loaders. |
| Medium | Prometheus families were interleaved and output used a shared predictable temporary path. | Group samples by family; write unique temporary files in the target directory, close, atomically rename and clean up. Test ordering, concurrent writers and failed publication. |

## Design decisions

- Preserve independent destination processing, explicit full-overwrite recovery,
  GUID-based incremental selection, bookmark-backed chains and unlimited transfer
  duration. Sender and receiver wire fields remain unchanged.
- Keep retention relative to the latest snapshot, including TXG ordering for
  snapshots with the same creation timestamp. Empty history does not expire with
  wall-clock time.
- A placeholder suffix is an ownership boundary on a filesystem. Distinct jobs
  and destinations must use distinct suffixes; automatic suffixes remain stable.
- `run` continues collecting module errors. Dry-run still permits monitoring
  output but performs no snapshot, deletion or send operations.
- A suspected final-batch evaluation defect was disproved by an executable test.
  The batching implementation is unchanged; regression coverage was added.

## Verification

- Regression tests reproduced confirmed selection, validation, command execution,
  receiver state, bookmark identity, cleanup and monitoring failures.
- Repository checks: `make unit-tests`, `go test -race -count=1 ./...`,
  `make build`, and `git diff --check`.
- Bats discovers all 24 integration cases. Local execution requires a delegated
  ZFS test dataset; none is configured or delegated to this user and passwordless
  sudo is unavailable. No existing datasets were modified for this review.
- The existing GitHub Actions workflow runs real ZFS integration after push.
