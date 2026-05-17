// Package config defines all user-facing configuration structs for zfsbackup,
// in JSON format. Each module reads a single JSON file that may contain a
// top-level Config object (for the unified "zfsbackup run" workflow) or an
// individual module section.
//
// # Unified config (recommended)
//
// One JSON file per backup job covers every module. Sections for modules you
// don't use may be omitted entirely.
//
//	{
//	  "include": ["mypool"],
//	  "exclude": ["mypool/scratch"],
//	  "snapshot": { "name_pattern": "snap-2006-01-02_15-04-05" },
//	  "sender":   { "snapshot_re": "snap-....-..-.._..-..-..", "destinations": [{"receiver": "ssh backup@host -- "}] },
//	  "deleter":  { "regex": ["snap-....-..-.._..-..-.." ], "preserve_top_n": 5, "rules": [{"interval":"1h","count":48}] },
//	  "monitor":  { "prometheus_output": "/var/lib/node_exporter/textfile_collector/zfsbackup.prom" }
//	}
//
// # Per-module include/exclude resolution
//
// The top-level Include/Exclude fields provide defaults that each module
// inherits unless that module supplies its own non-empty Include/Exclude. Use
// ResolveInclude and ResolveExclude to apply this logic.
//
// # Receiver config
//
// ReceiverConfig lives in a separate JSON file on the backup host — it is not
// part of Config because it is deployed on a different machine.
package config

// Config is the top-level unified configuration for a backup job. Pass it to
// zfs.LoadConfig and then inspect the module-specific sub-structs.
type Config struct {
	// Include lists root datasets whose full subtrees are included by default
	// in all modules that do not specify their own Include. Each entry must be
	// a valid ZFS dataset name; children are included recursively unless listed
	// in Exclude.
	Include []string `json:"include,omitempty"`

	// Exclude lists datasets whose full subtrees are excluded by default from
	// all modules that do not specify their own Exclude, even if they fall under
	// an included root.
	Exclude []string `json:"exclude,omitempty"`

	// Snapshot, if non-nil, enables the snapshot module for this job.
	Snapshot *SnapshotConfig `json:"snapshot,omitempty"`

	// Sender, if non-nil, enables the sender module for this job.
	Sender *SenderConfig `json:"sender,omitempty"`

	// Deleter, if non-nil, enables the deleter module for this job.
	Deleter *DeleterConfig `json:"deleter,omitempty"`

	// Monitor, if non-nil, enables the monitor module for this job.
	Monitor *MonitorConfig `json:"monitor,omitempty"`
}

// ResolveInclude returns moduleInclude if it is non-empty, otherwise c.Include.
// Use this in each module to determine the effective include list.
func (c *Config) ResolveInclude(moduleInclude []string) []string {
	if len(moduleInclude) > 0 {
		return moduleInclude
	}
	return c.Include
}

// ResolveExclude returns moduleExclude if it is non-empty, otherwise c.Exclude.
// Use this in each module to determine the effective exclude list.
func (c *Config) ResolveExclude(moduleExclude []string) []string {
	if len(moduleExclude) > 0 {
		return moduleExclude
	}
	return c.Exclude
}

// SnapshotConfig configures the snapshot module.
type SnapshotConfig struct {
	// Include overrides the top-level Config.Include for this module only.
	Include []string `json:"include,omitempty"`

	// Exclude overrides the top-level Config.Exclude for this module only.
	Exclude []string `json:"exclude,omitempty"`

	// NamePattern is a Go time-format string for snapshot names, e.g.
	// "snap-2006-01-02_15-04-05". Uses Go's reference time
	// (Mon Jan 2 15:04:05 MST 2006). The formatted result must be a valid ZFS
	// snapshot name component (no @, #, or spaces).
	NamePattern string `json:"name_pattern"`

	// SkipEmptyYoungerThan, when non-empty, skips creating a new snapshot for
	// a filesystem when both conditions hold: (a) the most recent existing
	// snapshot is younger than this duration, and (b) the filesystem has not
	// been written to since that snapshot (written=0). Prevents accumulating
	// empty snapshots on idle datasets. Supports ParseDuration suffixes:
	// s, m, h, d, w, y.
	SkipEmptyYoungerThan string `json:"skip_empty_younger_than,omitempty"`
}

// DestinationConfig configures a single backup destination within a sender job.
// A sender job may have multiple destinations; each one is processed
// independently for every filesystem in the job's include list.
type DestinationConfig struct {
	// Receiver is the command used to invoke the receiver. The sender splits
	// this string on whitespace and appends --op=<operation>, --dataset=<fs>,
	// and other flags as separate arguments, executing directly without a shell.
	//
	// Remote receiver over SSH:
	//   "ssh backup@host -- "
	// Local receiver on a second pool:
	//   "zfsbackup receiver --config /etc/zfsbackup/receiver.json -- "
	Receiver string `json:"receiver"`

	// Compression is the in-transit compression algorithm: "none" (default) or
	// "zstd". Applied on the sender side after the ZFS send stream is produced
	// and removed on the receiver side before zfs receive consumes it. Sender
	// and receiver must agree; a mismatch corrupts the stream.
	Compression string `json:"compression,omitempty"`

	// CompressionLevel overrides the compressor's built-in default level. Only
	// meaningful when Compression is "zstd".
	CompressionLevel *int `json:"compression_level,omitempty"`

	// RawSend transmits the stream in raw encrypted form (zfs send -e -c -w).
	// The receiver stores ciphertext and never sees the encryption key. Requires
	// native ZFS encryption on the source dataset.
	RawSend bool `json:"raw_send,omitempty"`

	// Placeholders lists placeholder bookmark suffixes to create on the source
	// after each successful send. A bookmark named #<snapname>-<suffix> is
	// created for each suffix, allowing the source snapshot to be pruned by the
	// deleter without breaking the incremental chain to this destination.
	//
	// When empty, a stable suffix is derived automatically from Receiver
	// (format: "dst" + 16 hex chars), so the incremental chain is protected
	// without any explicit configuration.
	//
	// Set explicitly for a human-readable name. When sending to multiple
	// destinations each entry in the job's Destinations list must use a distinct
	// suffix so their bookmarks do not collide. Suffixes must not contain
	// hyphens.
	Placeholders []string `json:"placeholders,omitempty"`

	// SyncPlaceholders lists placeholder bookmark suffixes to synchronise to
	// this receiver after each successful send. Enables multiple destinations to
	// share a common incremental base: destination B can start incremental sends
	// from the bookmark that destination A created.
	SyncPlaceholders []string `json:"sync_placeholders,omitempty"`

	// MbufferArgs lists arguments forwarded to mbuffer on the sender side to
	// smooth throughput over high-latency links. Each element may be a single
	// flag ("-q") or a flag-value pair ("-s 1M"); both are split on whitespace
	// before exec. Defaults to "-p 90 -m 3%" if unset and mbuffer is installed.
	MbufferArgs []string `json:"mbuffer_args,omitempty"`
}

// SenderConfig configures the sender module.
type SenderConfig struct {
	// Include overrides the top-level Config.Include for this module only.
	Include []string `json:"include,omitempty"`

	// Exclude overrides the top-level Config.Exclude for this module only.
	Exclude []string `json:"exclude,omitempty"`

	// Name is a human-readable label for this backup job, used in log output.
	Name string `json:"name,omitempty"`

	// SnapshotRegex is a regular expression matched against the snapshot name
	// component (without the dataset prefix). Only matching snapshots are
	// eligible for sending. Strongly recommended — without it, all snapshots on
	// the source are candidates.
	//
	// Example: "snap-....-..-.._..-..-.."
	SnapshotRegex string `json:"snapshot_re,omitempty"`

	// SendIntermediate, when true, sends all matching snapshots newer than the
	// current incremental base in order, so each destination accumulates a
	// complete history. When false (the default), only the most recent matching
	// snapshot is sent per run.
	SendIntermediate bool `json:"send_intermediate,omitempty"`

	// IncludeProperties includes ZFS property data in the stream (zfs send -p)
	// so each destination inherits source properties. Defaults to true if nil.
	IncludeProperties *bool `json:"include_properties,omitempty"`

	// Resumable enables resumable send/receive (zfs send/receive -s). An
	// interrupted transfer is resumed on the next run rather than restarted.
	// Defaults to true if nil. Must match the receiver's resumable setting.
	Resumable *bool `json:"resumable,omitempty"`

	// Destinations is the list of backup targets for this job. Each destination
	// is processed independently for every filesystem in the include list. At
	// least one destination is required.
	Destinations []DestinationConfig `json:"destinations"`
}

// RetentionRule keeps at most one snapshot per time interval for a fixed
// number of consecutive intervals, counted back from the most recent snapshot.
type RetentionRule struct {
	// Interval is the width of each retention bucket. Supports ParseDuration
	// suffixes: s, m, h, d, w, y. Example: "1h" retains one snapshot per hour;
	// "7d" retains one per week.
	Interval string `json:"interval"`

	// Count is the number of consecutive buckets to retain coverage for,
	// counting back from the most recent snapshot's time bucket.
	Count int64 `json:"count"`

	// AllowHoles, when true, tolerates gaps in interval coverage — time buckets
	// with no eligible snapshot are silently skipped rather than treated as an
	// error. Defaults to false. Leave false in normal operation so that
	// unexpected gaps surface as an error rather than silently reducing
	// retention.
	AllowHoles bool `json:"allow_holes,omitempty"`
}

// DeleterConfig configures the deleter module.
type DeleterConfig struct {
	// Include overrides the top-level Config.Include for this module only.
	Include []string `json:"include,omitempty"`

	// Exclude overrides the top-level Config.Exclude for this module only.
	Exclude []string `json:"exclude,omitempty"`

	// Regex lists regular expressions; only snapshots whose name (without the
	// dataset prefix) matches at least one pattern are eligible for deletion.
	// Snapshots matching no pattern are left untouched regardless of age.
	Regex []string `json:"regex,omitempty"`

	// PreserveTopN always preserves the N most recent matching snapshots,
	// regardless of age or rule evaluation. Evaluated before interval rules.
	// Zero means no unconditional preservation.
	PreserveTopN int64 `json:"preserve_top_n,omitempty"`

	// PreserveNewerThan, when non-empty, always preserves snapshots younger than
	// this duration, regardless of rule evaluation. The reference point is the
	// most recent snapshot's timestamp (the same frozen anchor used by interval
	// rules), not wall-clock time — so if snapshotting stops, the preservation
	// window freezes rather than silently expiring all recent snapshots on the
	// next deleter run. Supports ParseDuration suffixes: s, m, h, d, w, y.
	PreserveNewerThan string `json:"preserve_newer_than,omitempty"`

	// Rules lists interval-based retention rules. Each rule is evaluated
	// independently; a snapshot is kept if it is selected by any rule, by
	// PreserveTopN, or by PreserveNewerThan.
	Rules []RetentionRule `json:"rules,omitempty"`
}

// MonitorConfig configures the monitor module.
type MonitorConfig struct {
	// Include overrides the top-level Config.Include for this module only.
	Include []string `json:"include,omitempty"`

	// Exclude overrides the top-level Config.Exclude for this module only.
	Exclude []string `json:"exclude,omitempty"`

	// PrometheusOutput is the path where Prometheus text-format metrics are
	// written atomically (via a .tmp rename), e.g.
	// "/var/lib/node_exporter/textfile_collector/zfsbackup.prom". If empty,
	// metrics are printed to stdout only.
	PrometheusOutput string `json:"prometheus_output,omitempty"`
}

// ReceiverConfig is loaded from its own separate JSON file on the backup host.
// It is not part of Config because the receiver is deployed on a different
// machine.
type ReceiverConfig struct {
	// BaseDataset is the root dataset under which all received filesystems are
	// stored. The received dataset path is BaseDataset + "/" + source dataset
	// name. Example: BaseDataset "tank/backups/myhost" with source dataset
	// "mypool/data" produces destination "tank/backups/myhost/mypool/data".
	// Can be overridden per SSH key via --base_dataset on the command line.
	BaseDataset string `json:"base_dataset,omitempty"`

	// EnforceLocalProperties lists ZFS property names to strip from the
	// incoming stream and apply as local values instead, via -x flags to
	// zfs receive. Use for properties that should differ between source and
	// replica, such as mountpoint or quota.
	EnforceLocalProperties []string `json:"enforce_local_properties,omitempty"`

	// MbufferArgs lists arguments forwarded to mbuffer on the receiver side.
	// Same element format as SenderConfig.MbufferArgs. Defaults to
	// "-P 90 -m 3%" if unset and mbuffer is installed.
	MbufferArgs []string `json:"mbuffer_args,omitempty"`

	// DisableMount creates received datasets with canmount=off so they are not
	// auto-mounted. Defaults to true if nil. Set to false only if the replica
	// must be directly mountable.
	DisableMount *bool `json:"disable_mount,omitempty"`

	// Resumable enables zfs receive -s so interrupted transfers can be resumed
	// on the next run. Must match the sender's Resumable setting; a mismatch
	// causes transfer errors. Defaults to true if nil.
	Resumable *bool `json:"resumable,omitempty"`

	// ForceOverwriteDatasets lists destination datasets where zfs receive -F is
	// permitted, allowing a full send stream to overwrite an existing dataset.
	// Use only to recover from a broken incremental chain; remove each entry
	// once recovery is complete to prevent accidental overwrites.
	ForceOverwriteDatasets []string `json:"force_overwrite_datasets,omitempty"`
}
