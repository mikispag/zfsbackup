# 🗄️ zfsbackup

[![CI](https://github.com/mikispag/zfsbackup/actions/workflows/ci.yml/badge.svg)](https://github.com/mikispag/zfsbackup/actions/workflows/ci.yml)

Automated, incremental ZFS backups over SSH.

🔐 **Encrypted raw send** — the receiver stores ciphertext and never sees your key  
♻️ **Resumable transfers** — interrupted sends pick up where they left off  
🔖 **Placeholder bookmarks** — prune source snapshots without breaking incremental chains  
🗑️ **Configurable retention** — keep hourly, daily, weekly, and monthly snapshots  
📊 **Prometheus monitoring** — alert on stale snapshots before they matter  
📦 **Zero external dependencies** — pure Go stdlib, nothing to vendor or audit  

Five independent modules. Mix and match — use only what you need. Or run them all together with `zfsbackup run`.

---

## 🧩 Modules

| | Module | Purpose |
|:---:|---|---|
| 🚀 | **Run** | Runs all configured modules in sequence from a single config file |
| 📸 | **Snapshot** | Creates ZFS snapshots on a schedule |
| 📤 | **Sender** | Streams snapshots to a remote receiver over SSH |
| 📥 | **Receiver** | Accepts snapshot streams from a paired sender |
| 🗑️ | **Deleter** | Prunes old snapshots with configurable retention rules |
| 📊 | **Monitor** | Exports snapshot freshness metrics for Prometheus |

> [!NOTE]
> Sender and Receiver are paired — they must be used together. All other modules are independent.

> [!TIP]
> Logs from any module are available via `journalctl -u zfsbackup-<module>.service`.

---

## 🚀 Run (unified)

`zfsbackup run` reads a single JSON config file and runs all configured modules in sequence: snapshot → deleter → sender → monitor. Modules with no section in the config are skipped. All modules run regardless of individual failures; errors are collected and reported together at the end.

```sh
zfsbackup run --config /etc/zfsbackup/mypool.json
```

<details>
<summary>Systemd units</summary>

```ini
# /etc/systemd/system/zfsbackup.service
[Unit]
Description=ZFS Backup
After=network-online.target zfs.target
Wants=network-online.target

[Service]
Type=oneshot
TimeoutStartSec=0
ExecStart=/usr/local/bin/zfsbackup run --config /etc/zfsbackup/mypool.json --dry-run=false

# No [Install] section — this service is activated exclusively by the .timer unit
```

```ini
# /etc/systemd/system/zfsbackup.timer
[Unit]
Description=ZFS Backup — hourly

[Timer]
OnCalendar=hourly
Persistent=true

[Install]
WantedBy=timers.target
```

</details>

```sh
systemctl enable --now zfsbackup.timer
```

**mypool.json**
```json
{
  "include": ["mypool"],
  "exclude": ["mypool/scratch"],

  "snapshot": {
    "name_pattern": "snap-2006-01-02_15-04-05"
  },

  "sender": {
    "snapshot_re": "snap-....-..-.._..-..-..",
    "destinations": [
      {
        "receiver": "ssh username@backuphost -- ",
        "compression": "zstd",
        "placeholders": ["primary"]
      },
      {
        "receiver": "ssh username@offsite -- ",
        "compression": "zstd",
        "raw_send": true,
        "placeholders": ["offsite"]
      }
    ]
  },

  "deleter": {
    "regex": ["snap-....-..-.._..-..-.." ],
    "preserve_top_n": 5,
    "preserve_newer_than": "3h",
    "rules": [
      { "interval": "1h",  "count": 48, "allow_holes": true },
      { "interval": "1d",  "count": 30, "allow_holes": true },
      { "interval": "7d",  "count": 12 },
      { "interval": "30d", "count": 12 }
    ]
  },

  "monitor": {
    "prometheus_output": "/var/lib/node_exporter/textfile_collector/zfsbackup.prom"
  }
}
```

The top-level `include` and `exclude` fields provide defaults inherited by all modules. Individual module sections may override them with their own `include`/`exclude`.

---

## 📸 Snapshot

Takes snapshots of configured filesystems. Typically run every 30 minutes.

<details>
<summary>Systemd units</summary>

```ini
# /etc/systemd/system/zfsbackup-snapshot.service
[Unit]
Description=ZFS Backup snapshot
After=zfs.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/zfsbackup snapshot --config /etc/zfsbackup/mypool.json
# User=backupuser  # uncomment if using ZFS delegation

# No [Install] section — this service is activated exclusively by the .timer unit
```

```ini
# /etc/systemd/system/zfsbackup-snapshot.timer
[Unit]
Description=ZFS Backup snapshot — every 30 minutes

[Timer]
OnCalendar=*:0/30
Persistent=true

[Install]
WantedBy=timers.target
```

</details>

```sh
systemctl enable --now zfsbackup-snapshot.timer
```

**snapshot section in mypool.json**
```json
{
  "include": ["mypool"],
  "exclude": ["mypool/nobackup"],
  "snapshot": {
    "name_pattern": "snap-2006-01-02_15-04-05"
  }
}
```

> [!TIP]
> `name_pattern` uses Go's reference time: `Mon Jan 2 15:04:05 MST 2006`. With ZFS delegation, no root required.

---

## 🗑️ Deleter

Prunes snapshots according to configurable retention rules. Typically run hourly.

<details>
<summary>Systemd units</summary>

```ini
# /etc/systemd/system/zfsbackup-deleter.service
[Unit]
Description=ZFS Backup deleter
After=zfs.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/zfsbackup deleter --config /etc/zfsbackup/mypool.json --dry-run=false
# User=backupuser  # uncomment if using ZFS delegation

# No [Install] section — this service is activated exclusively by the .timer unit
```

```ini
# /etc/systemd/system/zfsbackup-deleter.timer
[Unit]
Description=ZFS Backup deleter — hourly

[Timer]
OnCalendar=hourly
Persistent=true

[Install]
WantedBy=timers.target
```

</details>

```sh
systemctl enable --now zfsbackup-deleter.timer
```

> [!CAUTION]
> `--dry-run` defaults to `true`. The example service file passes `--dry-run=false` explicitly — remove that flag to run in dry-run mode for testing.

**deleter section in mypool.json**
```json
{
  "include": ["mypool"],
  "exclude": ["mypool/nobackup"],
  "deleter": {
    "regex": ["snap-....-..-.._..-..-.." ],
    "preserve_top_n": 5,
    "preserve_newer_than": "3h",
    "rules": [
      { "interval": "1h",  "count": 48, "allow_holes": true },
      { "interval": "1d",  "count": 30, "allow_holes": true },
      { "interval": "7d",  "count": 12 },
      { "interval": "30d", "count": 12 }
    ]
  }
}
```

The above policy retains:

| Granularity | Coverage |
|---|---|
| 🔒 Always | The 5 newest snapshots + anything younger than 3 hours |
| ⏱️ Hourly | 1 per hour for the last 48 hours |
| 📅 Daily | 1 per day for the last 30 days |
| 🗓️ Weekly | 1 per week for the last 12 weeks |
| 📆 Monthly | 1 per month for the last year |

> [!NOTE]
> Rules are evaluated relative to the **most recent snapshot**, not the current time — so if snapshotting stops, old snapshots won't be pruned unexpectedly. `allow_holes: true` skips missing intervals instead of aborting. With ZFS delegation, no root required.

---

## 📤 Sender

Sends incremental snapshot streams to a remote receiver. Typically run hourly.

<details>
<summary>Systemd units</summary>

```ini
# /etc/systemd/system/zfsbackup-sender.service
[Unit]
Description=ZFS Backup sender
After=network-online.target zfs.target
Wants=network-online.target

[Service]
Type=oneshot
TimeoutStartSec=0
ExecStart=/usr/local/bin/zfsbackup sender --config /etc/zfsbackup/mypool.json

# No [Install] section — this service is activated exclusively by the .timer unit
```

```ini
# /etc/systemd/system/zfsbackup-sender.timer
[Unit]
Description=ZFS Backup sender — hourly

[Timer]
OnCalendar=hourly
Persistent=true

[Install]
WantedBy=timers.target
```

</details>

```sh
systemctl enable --now zfsbackup-sender.timer
```

**sender section in mypool.json**
```json
{
  "include": ["mypool"],
  "exclude": ["mypool/scratch"],
  "sender": {
    "name": "send_to_backupservers",
    "snapshot_re": "snap-....-..-.._..-..-..",
    "destinations": [
      {
        "receiver": "ssh username@primary -- ",
        "compression": "zstd",
        "placeholders": ["primary"]
      },
      {
        "receiver": "ssh username@offsite -- ",
        "compression": "zstd",
        "raw_send": true,
        "placeholders": ["offsite"]
      }
    ]
  }
}
```

Each entry in `destinations` is processed independently for every included filesystem in a single `zfsbackup sender` run. Each destination maintains its own placeholder bookmarks, so the deleter can prune source snapshots while incremental chains to all destinations remain intact.

The `receiver` field is the command used to invoke the receiver — typically an SSH invocation (the example assumes `ForceCommand` is set for the SSH key on the remote side). For local backup to another pool:

```json
"receiver": "zfsbackup receiver --config /etc/zfsbackup/receiver.json -- "
```

> [!NOTE]
> Placeholder bookmarks are created automatically after every successful send. A bookmark named `#<snap>-dst<hash>` is created on the source for each destination, derived from `receiver`, so source snapshots can be safely pruned by the deleter without breaking the incremental chain to any destination. No configuration is needed.
>
> Set `placeholders` explicitly only when you need a human-readable name. Each destination must use a distinct suffix.

**Key sender-level options** (apply to all destinations):

| Option | Description |
|---|---|
| `snapshot_re` | 🔍 Regex filter — only matching snapshot names are considered. Strongly recommended |
| `send_intermediate` | 📋 Send all matching snapshots in order (default: newest only) |
| `include_properties` | 📦 Include ZFS properties in the stream (default: `true`) |
| `resumable` | ♻️ Enable resumable transfers (default: `true`) |

**Key per-destination options** (set inside each `destinations` entry):

| Option | Description |
|---|---|
| `receiver` | 🔌 Command to invoke the receiver |
| `raw_send` | 🔐 Use `zfs send -ecw` — receiver stores ciphertext and never sees the encryption key |
| `compression: "zstd"` | ⚡ Compress the stream in transit (sender and receiver must agree) |
| `mbuffer_args` | 🔄 Smooth throughput over high-latency links via [mbuffer](https://www.maier-komor.de/mbuffer.html) |
| `placeholders` | 🔖 Override the auto-derived placeholder suffix |
| `sync_placeholders` | 🔗 Sync placeholder bookmarks to this receiver |

---

## 📥 Receiver

Deployed as an SSH forced command in `authorized_keys` on the backup host:

```
command="zfsbackup receiver --base_dataset=backuppool/accounts/myhost --config=/etc/zfsbackup/receiver.json",no-X11-forwarding,no-port-forwarding,no-agent-forwarding,no-pty ssh-ed25519 AAAA... keyname
```

`--base_dataset` sets the root dataset under which received filesystems are stored — it overrides the config file value and can be set per authorized key.

**receiver.json** is optional. Use it to configure:

| Option | Description |
|---|---|
| `base_dataset` | Root destination dataset |
| `mbuffer_args` | Buffer on the receive side for smoother throughput |
| `enforce_local_properties` | ZFS properties to strip from the stream and set locally |
| `disable_mount` | Pass `-u -o canmount=off` to `zfs receive` so the dataset is never mounted now or on future `zfs mount -a` / boot (default: `true`) |

```json
{
  "base_dataset": "tank/backups/myhost",
  "disable_mount": true,
  "enforce_local_properties": ["mountpoint"]
}
```

---

## 📊 Monitor

Checks the age of the newest snapshot for each configured filesystem and exports Prometheus metrics via the Node Exporter textfile collector.

<details>
<summary>Systemd units</summary>

```ini
# /etc/systemd/system/zfsbackup-monitor.service
[Unit]
Description=ZFS Backup monitor
After=zfs.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/zfsbackup monitor --config /etc/zfsbackup/mypool.json
```

```ini
# /etc/systemd/system/zfsbackup-monitor.timer
[Unit]
Description=ZFS Backup monitor — every 4 hours

[Timer]
OnCalendar=0/4:00:00
Persistent=true

[Install]
WantedBy=timers.target
```

</details>

```sh
systemctl enable --now zfsbackup-monitor.timer
```

**monitor section in mypool.json**
```json
{
  "include": ["mypool/important"],
  "monitor": {
    "prometheus_output": "/var/lib/node_exporter/textfile_collector/zfsbackup.prom"
  }
}
```

Write your own alerting rules against the exported `LastSnapAge` and `LastSnapTimestamp` metrics.

> [!NOTE]
> No root required if `zpool` is in PATH. On many distributions it is root-only — check yours.

---

## 📥 Installation

```sh
go install github.com/mikispag/zfsbackup/cmd/zfsbackup@latest
```

Or build from source:

```sh
git clone https://github.com/mikispag/zfsbackup
cd zfsbackup
make build
```

---

## 🧪 Running the test suite

1. Create and delegate a ZFS filesystem for tests:

   ```sh
   zfs create mypool/zfsbackuptestsuite
   zfs allow -ldu testsuiteuser \
     bookmark,canmount,change-key,compression,create,destroy,diff,encryption,\
keyformat,keylocation,load-key,logbias,mount,mountpoint,promote,readonly,\
receive,rename,rollback,send,snapshot,userprop \
     mypool/zfsbackuptestsuite
   export DELEGATED_FS=mypool/zfsbackuptestsuite
   ```

2. Install [bats-core](https://github.com/bats-core/bats-core).

3. Run `make tests`.
