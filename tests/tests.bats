TESTSPATH=$BATS_TEST_DIRNAME
BASEPATH=`realpath $TESTSPATH/../`
SSH_TEST_PATH=`realpath $TESTSPATH/ssh/`
FAKESNAPS_PATH=`realpath $TESTSPATH/fakesnaps/`
DELEGATED_FS="${DELEGATED_FS:?}"
load 'libs/bats-support/load'
load 'libs/bats-assert/load'


setup_file(){
echo $TESTSPATH
echo $BASEPATH
echo $SSH_TEST_PATH
export TEST_TMP_DIR=$(mktemp -d)
export SSH_TEST_PATH
(cd $BASEPATH; CGO_ENABLED=0 go build -o $TEST_TMP_DIR/zfsbackup ./cmd/zfsbackup)
make -C $SSH_TEST_PATH start_server
make -C $FAKESNAPS_PATH gensnaps
export TESTFS=${DELEGATED_FS}/suite${RANDOM}
export SENDERFS=$TESTFS/sender
export RECEIVERFS=$TESTFS/receiver/$TESTFS/sender

# Receiver is deployed as an SSH ForceCommand; the ForceCommand template uses
# REPLACEME as a placeholder replaced here.
sed -i "s~REPLACEME~$TEST_TMP_DIR/zfsbackup receiver --config=$TEST_TMP_DIR/receiver.json --base_dataset=$TESTFS/receiver~" $SSH_TEST_PATH/authorized_keys

# receiver_limited.sh wraps SSH with a 2 MB data cap to simulate an
# interrupted transfer for the resumable-send test.
cat >$TEST_TMP_DIR/receiver_limited.sh <<'SCRIPT'
#!/bin/bash
head -c $((2*1024*1024)) | exec "$@"
SCRIPT
chmod +x $TEST_TMP_DIR/receiver_limited.sh

# Default sender config.
cat >$TEST_TMP_DIR/sender.json <<EOF
{
  "include": ["$TESTFS/sender"],
  "exclude": ["$TESTFS/sender/excluded"],
  "sender": {
    "snapshot_re": "snap_remote.*",
    "destinations": [
      {"receiver": "ssh -F $SSH_TEST_PATH/ssh_config -- test_target"}
    ]
  }
}
EOF

# Sender that truncates the stream at 2 MB to trigger resumable receive.
cat >$TEST_TMP_DIR/sender_breakafter2m.json <<EOF
{
  "include": ["$TESTFS/sender"],
  "exclude": ["$TESTFS/sender/excluded"],
  "sender": {
    "snapshot_re": "snap_remote.*",
    "resumable": true,
    "destinations": [
      {"receiver": "$TEST_TMP_DIR/receiver_limited.sh ssh -F $SSH_TEST_PATH/ssh_config -- test_target"}
    ]
  }
}
EOF

# Sender with include_properties disabled.
cat >$TEST_TMP_DIR/sender_noprops.json <<EOF
{
  "include": ["$TESTFS/sender"],
  "exclude": ["$TESTFS/sender/excluded"],
  "sender": {
    "snapshot_re": "snap_remote.*",
    "include_properties": false,
    "destinations": [
      {"receiver": "ssh -F $SSH_TEST_PATH/ssh_config -- test_target"}
    ]
  }
}
EOF

# Sender that sends all matching snapshots in order (send_intermediate).
cat >$TEST_TMP_DIR/sender_all.json <<EOF
{
  "include": ["$TESTFS/sender"],
  "exclude": ["$TESTFS/sender/excluded"],
  "sender": {
    "snapshot_re": "snap_remote.*",
    "send_intermediate": true,
    "destinations": [
      {"receiver": "ssh -F $SSH_TEST_PATH/ssh_config -- test_target"}
    ]
  }
}
EOF

# Sender with zstd compression at level 4.
cat >$TEST_TMP_DIR/sender_compressed.json <<EOF
{
  "include": ["$TESTFS/sender"],
  "exclude": ["$TESTFS/sender/excluded"],
  "sender": {
    "snapshot_re": "snap_remote.*",
    "destinations": [
      {
        "receiver": "ssh -F $SSH_TEST_PATH/ssh_config -- test_target",
        "compression": "zstd",
        "compression_level": 4
      }
    ]
  }
}
EOF

# Sender with zstd compression and raw encrypted send.
cat >$TEST_TMP_DIR/sender_raw.json <<EOF
{
  "include": ["$TESTFS/sender"],
  "exclude": ["$TESTFS/sender/excluded"],
  "sender": {
    "snapshot_re": "snap_remote.*",
    "destinations": [
      {
        "receiver": "ssh -F $SSH_TEST_PATH/ssh_config -- test_target",
        "compression": "zstd",
        "compression_level": 4,
        "raw_send": true
      }
    ]
  }
}
EOF

# Sender that sets a named placeholder bookmark after each send.
cat >$TEST_TMP_DIR/sender_ph_one.json <<EOF
{
  "include": ["$TESTFS/sender"],
  "exclude": ["$TESTFS/sender/excluded"],
  "sender": {
    "snapshot_re": "snap_remote.*",
    "destinations": [
      {
        "receiver": "ssh -F $SSH_TEST_PATH/ssh_config -- test_target",
        "placeholders": ["testsendjob"]
      }
    ]
  }
}
EOF

# Sender with mbuffer buffering.
cat >$TEST_TMP_DIR/sender_buffer.json <<EOF
{
  "include": ["$TESTFS/sender"],
  "exclude": ["$TESTFS/sender/excluded"],
  "sender": {
    "snapshot_re": "snap_remote.*",
    "destinations": [
      {
        "receiver": "ssh -F $SSH_TEST_PATH/ssh_config -- test_target",
        "mbuffer_args": ["-p 20"]
      }
    ]
  }
}
EOF

# Standalone receiver config (deployed on the backup host via ForceCommand).
cat >$TEST_TMP_DIR/receiver.json <<EOF
{
  "mbuffer_args": ["-P 80"],
  "enforce_local_properties": ["compression", "testprop:keep"],
  "resumable": true,
  "force_overwrite_datasets": ["$TESTFS/receiver/$SENDERFS/forceoverwrite"]
}
EOF

# Monitor config.
cat >$TEST_TMP_DIR/monitor.json <<EOF
{
  "include": ["$TESTFS/sender/mypool"],
  "exclude": ["$TESTFS/sender/mypool/skipthis"],
  "monitor": {
    "prometheus_output": "metrics.test"
  }
}
EOF
}

teardown_file(){
kill $(cat $TEST_TMP_DIR/sshd.pid)
[ -d $TEST_TMP_DIR ] && rm -rf $TEST_TMP_DIR
}

setup(){
zfs create -o canmount=off -o mountpoint=legacy $TESTFS
zfs create -o canmount=off -o mountpoint=legacy $TESTFS/receiver
}

teardown(){
zfs destroy -r $TESTFS
}

@test "SendFullThenIncremental" {
  zfs create -p -o canmount=off $TESTFS/sender/mypool/myds
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote1
  zfs snapshot $TESTFS/sender/mypool/myds@snap2
  run zfs list -t snapshot $TESTFS/sender/mypool/myds -o name -H
  assert_output <<EOM
$SENDERFS/mypool/myds@snap_remote1
$SENDERFS/mypool/myds@snap2
EOM
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$RECEIVERFS/mypool/myds@snap_remote1
EOM
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote2
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$RECEIVERFS/mypool/myds@snap_remote1
$RECEIVERFS/mypool/myds@snap_remote2
EOM
}

@test "SendWithLimitFs" {
  zfs create -p -o canmount=off $TESTFS/sender/mypool/myds
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote1
  zfs create -p -o canmount=off $TESTFS/sender/mypool/myds2
  zfs snapshot $TESTFS/sender/mypool/myds2@snap_remote1
  run zfs list -r -d 2 -t snapshot $TESTFS/sender/mypool -o name -H
  assert_output <<EOM
$SENDERFS/mypool/myds@snap_remote1
$SENDERFS/mypool/myds2@snap_remote1
EOM
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender.json --limit-fs=$SENDERFS/mypool/myds2
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$RECEIVERFS/mypool/myds2@snap_remote1
EOM
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$RECEIVERFS/mypool/myds@snap_remote1
$RECEIVERFS/mypool/myds2@snap_remote1
EOM
}

@test "ReceiveWithForceOverwrite" {
  zfs create -p -o canmount=off $TESTFS/sender/forceoverwrite
  zfs snapshot $TESTFS/sender/forceoverwrite@snap_remote1
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$RECEIVERFS/forceoverwrite@snap_remote1
EOM
  zfs destroy $RECEIVERFS/forceoverwrite@snap_remote1
  zfs destroy -r $TESTFS/sender/forceoverwrite
  zfs create -p -o canmount=off $TESTFS/sender/forceoverwrite
  zfs snapshot $TESTFS/sender/forceoverwrite@snap_remote2
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$RECEIVERFS/forceoverwrite@snap_remote2
EOM
}

@test "SendProps" {
  zfs create -p -o canmount=off -o normalization=formKC $TESTFS/sender/mypool/myds
  MYNUMBER=$RANDOM
  zfs set myprop:something=$MYNUMBER $TESTFS/sender/mypool/myds
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote1
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender.json
  assert_success
  run zfs get myprop:something $RECEIVERFS/mypool/myds
  assert_success
  assert_output -e <<EOM
$RECEIVERFS/mypool/myds\s*myprop:something\s*${MYNUMBER}\s*received
EOM
  run zfs get normalization $RECEIVERFS/mypool/myds
  assert_success
  assert_output -e <<EOM
$RECEIVERFS/mypool/myds\s*normalization\s*formKC
EOM
  zfs set myprop:something=firstchange $TESTFS/sender/mypool/myds
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote2
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender.json
  assert_success
  run zfs get myprop:something $RECEIVERFS/mypool/myds
  assert_success
  assert_output -e <<EOM
$RECEIVERFS/mypool/myds\s*myprop:something\s*firstchange\s*received
EOM
  zfs set myprop:something=secondchange $TESTFS/sender/mypool/myds
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote3
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_noprops.json
  assert_success
  run zfs get myprop:something $RECEIVERFS/mypool/myds
  assert_success
  # include_properties=false: property should not have updated to secondchange.
  assert_output -e <<EOM
$RECEIVERFS/mypool/myds\s*myprop:something\s*firstchange\s*received
EOM
}

@test "SendWithBuffer" {
  zfs create -p -o canmount=off $TESTFS/sender/mypool/myds
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote1
  zfs snapshot $TESTFS/sender/mypool/myds@snap2
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_buffer.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$RECEIVERFS/mypool/myds@snap_remote1
EOM
}

@test "LocalProperty" {
  zfs create -p -o canmount=off -o compression=gzip-1 -o logbias=throughput $TESTFS/sender/mypool/myds
  zfs set testprop:keep=sendervalue $TESTFS/sender/mypool/myds
  zfs set testprop:overwrite=sendervalue $TESTFS/sender/mypool/myds
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote1
  zfs set compression=gzip-9 $TESTFS/receiver
  zfs set logbias=latency $TESTFS/receiver
  zfs set testprop:keep=receivervalue $TESTFS/receiver
  zfs set testprop:overwrite=receivervalue $TESTFS/receiver
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender.json
  assert_success
  run zfs list -t filesystem -r $TESTFS/receiver -o name,compression,logbias,testprop:keep,testprop:overwrite -H
  assert_output -e<<EOM
$RECEIVERFS/mypool/myds\s*gzip-9\s*throughput\s*receivervalue\s*sendervalue
EOM
}

@test "MakeSnapshots" {
  cat >$TEST_TMP_DIR/snapshot.json <<EOF
{
  "include": ["$TESTFS/sender/mypool"],
  "exclude": ["$TESTFS/sender/mypool/myds/sub2"],
  "snapshot": {
    "name_pattern": "my_new_snap_2006_01_02"
  }
}
EOF
  TODAY=$(date +%Y_%m_%d)
  zfs create -p -o canmount=off $TESTFS/sender/mypool/myds
  zfs create -p -o canmount=off $TESTFS/sender/mypool/myds/sub1
  zfs create -p -o canmount=off $TESTFS/sender/mypool/myds/sub2
  zfs create -p -o canmount=off $TESTFS/sender/mypool/myds/sub2/subsub1
  zfs create -p -o canmount=off $TESTFS/sender/mypool/myds/sub3
  zfs create -p -o canmount=off $TESTFS/sender/mypool/myds/sub3/subsub1
  run $TEST_TMP_DIR/zfsbackup snapshot --config=$TEST_TMP_DIR/snapshot.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/sender -o name -H
  assert_output <<EOM
$TESTFS/sender/mypool@my_new_snap_$TODAY
$TESTFS/sender/mypool/myds@my_new_snap_$TODAY
$TESTFS/sender/mypool/myds/sub1@my_new_snap_$TODAY
$TESTFS/sender/mypool/myds/sub3@my_new_snap_$TODAY
$TESTFS/sender/mypool/myds/sub3/subsub1@my_new_snap_$TODAY
EOM
}

@test "MakeSnapshotsSkipEmpty" {
  cat >$TEST_TMP_DIR/snapshot.json <<EOF
{
  "include": ["$TESTFS/sender/mypool"],
  "snapshot": {
    "name_pattern": "my_new_snap_2006_01_02",
    "skip_empty_younger_than": "3d"
  }
}
EOF
  TODAY=$(date +%Y_%m_%d)
  zfs create -p -o canmount=off $TESTFS/sender/mypool/myds1_nosnap
  times=(
  "5 days ago"
  "4 days ago"
  "3 days ago"
  "2 days ago"
  "1 day ago"
  )
  make_snaps $TESTFS/sender/mypool/myds2_newsnap "${times[@]}"
  times=(
  "5 days ago"
  "4 days ago"
  )
  make_snaps $TESTFS/sender/mypool/myds3_oldsnap "${times[@]}"
  run $TEST_TMP_DIR/zfsbackup snapshot --config=$TEST_TMP_DIR/snapshot.json
  assert_success
  run bash -c "zfs list -t snapshot -r $TESTFS/sender -o name -H |grep my_new_snap"
  # myds2 is skipped: it has a recent snapshot (1 day ago) and written=0.
  assert_output <<EOM
$TESTFS/sender/mypool@my_new_snap_$TODAY
$TESTFS/sender/mypool/myds1_nosnap@my_new_snap_$TODAY
$TESTFS/sender/mypool/myds3_oldsnap@my_new_snap_$TODAY
EOM
}

@test "IncrementalsSkipsToLast" {
  zfs create -p -o canmount=off $TESTFS/sender/mypool/myds
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote1
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$RECEIVERFS/mypool/myds@snap_remote1
EOM
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote2
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote3
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote4
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote5
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$RECEIVERFS/mypool/myds@snap_remote1
$RECEIVERFS/mypool/myds@snap_remote5
EOM
  # Another run should not fail even if everything is already synced.
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender.json
  assert_success
}

@test "IncrementalsSendThemAll" {
  zfs create -p -o canmount=off $TESTFS/sender/mypool/myds
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote1
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_all.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$RECEIVERFS/mypool/myds@snap_remote1
EOM
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote2
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote3
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote4
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote5
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_all.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$RECEIVERFS/mypool/myds@snap_remote1
$RECEIVERFS/mypool/myds@snap_remote2
$RECEIVERFS/mypool/myds@snap_remote3
$RECEIVERFS/mypool/myds@snap_remote4
$RECEIVERFS/mypool/myds@snap_remote5
EOM
  # Another run should not fail even if everything is already synced.
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_all.json
  assert_success
}

@test "SendFullCompressed" {
  zfs create -p -o canmount=off $TESTFS/sender/mypool/myds
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote1
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_compressed.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$RECEIVERFS/mypool/myds@snap_remote1
EOM
}

@test "Exclusions" {
  zfs create -p -o canmount=off $TESTFS/sender/mypool/myds
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote1
  zfs create -p -o canmount=off $TESTFS/sender/excluded/myds2
  zfs snapshot $TESTFS/sender/excluded/myds2@snap_remote1
  run zfs list -t snapshot -r $TESTFS/sender -o name -H
  assert_output <<EOM
$SENDERFS/excluded/myds2@snap_remote1
$SENDERFS/mypool/myds@snap_remote1
EOM
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$RECEIVERFS/mypool/myds@snap_remote1
EOM
}

@test "SendFromBookmark" {
  zfs create -p -o canmount=off $TESTFS/sender/mypool/myds
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote1
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$RECEIVERFS/mypool/myds@snap_remote1
EOM
  zfs bookmark $TESTFS/sender/mypool/myds@snap_remote1 $TESTFS/sender/mypool/myds#snap_remote1
  zfs destroy $TESTFS/sender/mypool/myds@snap_remote1
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote2
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$RECEIVERFS/mypool/myds@snap_remote1
$RECEIVERFS/mypool/myds@snap_remote2
EOM
}

@test "SendFromResumeToken" {
  zfs create -p -o canmount=off $TESTFS/sender/mypool
  zfs receive -u -o canmount=off $TESTFS/sender/mypool/myds < $TESTSPATH/samplefs.zfs
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote1
  zfs create -p -o canmount=off $RECEIVERFS/mypool
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_breakafter2m.json
  assert_failure
  assert_output --partial <<EOM
Partially received snapshot is saved.
EOM
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_breakafter2m.json
  assert_failure
  assert_output --partial <<EOM
Partially received snapshot is saved.
EOM
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_breakafter2m.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output --partial "$RECEIVERFS/mypool/myds@snap_remote1"
}

@test "RawSendFullThenIncremental" {
  echo 12345678|zfs create -p -o canmount=off -o encryption=on -o keyformat=passphrase -o keylocation=prompt $TESTFS/sender/mypool/myencryptedds
  zfs snapshot $TESTFS/sender/mypool/myencryptedds@snap_remote1
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_raw.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$RECEIVERFS/mypool/myencryptedds@snap_remote1
EOM
  zfs snapshot $TESTFS/sender/mypool/myencryptedds@snap_remote2
  zfs unload-key $TESTFS/sender/mypool/myencryptedds
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_raw.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$RECEIVERFS/mypool/myencryptedds@snap_remote1
$RECEIVERFS/mypool/myencryptedds@snap_remote2
EOM
  run zfs get all $RECEIVERFS/mypool/myencryptedds
  assert_output --partial <<EOM
$RECEIVERFS/mypool/myencryptedds  encryptionroot        $RECEIVERFS/mypool/myencryptedds  -
EOM
  assert_output --partial <<EOM
$RECEIVERFS/mypool/myencryptedds  keystatus             unavailable
EOM
}

@test "SendSetPlaceholders" {
  zfs create -p -o canmount=off $TESTFS/sender/mypool/myds
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote1
  zfs snapshot $TESTFS/sender/mypool/myds@snap2
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_ph_one.json
  assert_success

  run zfs list -t snapshot,bookmark $TESTFS/sender/mypool/myds -o name -H -r -d1
  assert_output <<EOM
$SENDERFS/mypool/myds@snap_remote1
$SENDERFS/mypool/myds@snap2
$SENDERFS/mypool/myds#snap_remote1-testsendjob
EOM

  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$RECEIVERFS/mypool/myds@snap_remote1
EOM
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote2
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_ph_one.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$RECEIVERFS/mypool/myds@snap_remote1
$RECEIVERFS/mypool/myds@snap_remote2
EOM
  run zfs list -t snapshot,bookmark $TESTFS/sender/mypool/myds -o name -H -r -d1
  assert_output <<EOM
$SENDERFS/mypool/myds@snap_remote1
$SENDERFS/mypool/myds@snap2
$SENDERFS/mypool/myds@snap_remote2
$SENDERFS/mypool/myds#snap_remote2-testsendjob
EOM
}

@test "PlaceholdersForMultipleDest" {
  cat >$TEST_TMP_DIR/sender_dest1.json <<EOF
{
  "include": ["$TESTFS/sender"],
  "sender": {
    "snapshot_re": ".*",
    "destinations": [
      {
        "receiver": "$TEST_TMP_DIR/zfsbackup receiver --base_dataset=$TESTFS/receiver/dest1 -- ",
        "placeholders": ["dest1"],
        "sync_placeholders": ["dest2"]
      }
    ]
  }
}
EOF
  cat >$TEST_TMP_DIR/sender_dest2.json <<EOF
{
  "include": ["$TESTFS/sender"],
  "sender": {
    "snapshot_re": ".*",
    "destinations": [
      {
        "receiver": "$TEST_TMP_DIR/zfsbackup receiver --base_dataset=$TESTFS/receiver/dest2 -- ",
        "placeholders": ["dest2"]
      }
    ]
  }
}
EOF
  for FS in $TESTFS/sender/mypool/myds $TESTFS/receiver/dest1 $TESTFS/receiver/dest2; do
    zfs create -p -o canmount=off $FS
  done
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote1
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_dest2.json
  assert_success
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_dest1.json
  assert_success

  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote2
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_dest1.json
  assert_success
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote3
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_dest1.json
  assert_success
  zfs destroy $TESTFS/sender/mypool/myds@snap_remote1
  run zfs list -t snapshot,bookmark $TESTFS/sender/mypool/myds -o name -H -r -d1
  assert_output <<EOM
$SENDERFS/mypool/myds@snap_remote2
$SENDERFS/mypool/myds@snap_remote3
$SENDERFS/mypool/myds#snap_remote1-dest2
$SENDERFS/mypool/myds#snap_remote3-dest1
EOM
  run zfs list -t snapshot,bookmark -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$TESTFS/receiver/dest1/$SENDERFS/mypool/myds@snap_remote1
$TESTFS/receiver/dest1/$SENDERFS/mypool/myds@snap_remote2
$TESTFS/receiver/dest1/$SENDERFS/mypool/myds@snap_remote3
$TESTFS/receiver/dest1/$SENDERFS/mypool/myds#snap_remote1-dest2
$TESTFS/receiver/dest2/$SENDERFS/mypool/myds@snap_remote1
EOM
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_dest2.json
  assert_success
  run zfs list -t snapshot,bookmark -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$TESTFS/receiver/dest1/$SENDERFS/mypool/myds@snap_remote1
$TESTFS/receiver/dest1/$SENDERFS/mypool/myds@snap_remote2
$TESTFS/receiver/dest1/$SENDERFS/mypool/myds@snap_remote3
$TESTFS/receiver/dest1/$SENDERFS/mypool/myds#snap_remote1-dest2
$TESTFS/receiver/dest2/$SENDERFS/mypool/myds@snap_remote1
$TESTFS/receiver/dest2/$SENDERFS/mypool/myds@snap_remote3
EOM
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_dest1.json
  assert_success
  run zfs list -t snapshot,bookmark -r $TESTFS/receiver -o name -H
  assert_output <<EOM
$TESTFS/receiver/dest1/$SENDERFS/mypool/myds@snap_remote1
$TESTFS/receiver/dest1/$SENDERFS/mypool/myds@snap_remote2
$TESTFS/receiver/dest1/$SENDERFS/mypool/myds@snap_remote3
$TESTFS/receiver/dest1/$SENDERFS/mypool/myds#snap_remote3-dest2
$TESTFS/receiver/dest2/$SENDERFS/mypool/myds@snap_remote1
$TESTFS/receiver/dest2/$SENDERFS/mypool/myds@snap_remote3
EOM
}

make_snaps(){
  FS=$1
  shift
  zfs create -p -o canmount=off $FS
  zfs snapshot $FS@firstsnap
  GUID=$(zfs get guid -H -o value $FS@firstsnap)
  NEXT_GUID=10000000
  ID=1
  for T in "$@"; do
    TIME=$(date --date="$T" +%s)
    T_TEXT="$(echo -n $T | tr -c a-zA-Z0-9 _)"
    $FAKESNAPS_PATH/gensnaps $TIME $GUID ${NEXT_GUID} snap-$ID-$T_TEXT |zfs receive $FS
    GUID=$NEXT_GUID
    NEXT_GUID=$(($NEXT_GUID + 1))
    ID=$(($ID + 1))
  done
  zfs destroy $FS@firstsnap
}

@test "TestInfra:SyntethicSnapWorks" {
  make_snaps $TESTFS/sender/mypool/fs_many_snaps "2000-01-01" "2010-01-01"
  run zfs list -H -p -t snapshot $TESTFS/sender/mypool/fs_many_snaps -o name,creation,guid -H
  assert_output -e <<EOM
$TESTFS/sender/mypool/fs_many_snaps@snap-1-2000_01_01\s+946681200\s+10000000
$TESTFS/sender/mypool/fs_many_snaps@snap-2-2010_01_01\s+1262300400\s+10000001
EOM
}

@test "SendToMultipleDestinations_OneFailsOtherSucceeds" {
  # Failure isolation: if one destination is broken the other must still receive
  # its snapshots and the run must exit with a non-zero status.
  zfs create -p -o canmount=off $TESTFS/receiver/good
  zfs create -p -o canmount=off $TESTFS/sender/mypool/myds
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote1
  cat >$TEST_TMP_DIR/sender_onebad.json <<EOF
{
  "include": ["$TESTFS/sender"],
  "sender": {
    "snapshot_re": "snap_remote.*",
    "destinations": [
      {
        "receiver": "$TEST_TMP_DIR/zfsbackup receiver --base_dataset=$TESTFS/receiver/good -- ",
        "placeholders": ["good"]
      },
      {
        "receiver": "false -- ",
        "placeholders": ["bad"]
      }
    ]
  }
}
EOF
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_onebad.json
  assert_failure
  # The good destination must still have received the snapshot.
  run zfs list -t snapshot -r $TESTFS/receiver/good -o name -H
  assert_output "$TESTFS/receiver/good/$SENDERFS/mypool/myds@snap_remote1"
}

@test "SendToMultipleDestinations" {
  # Exercises the multiple-destinations feature: a single sender config sends
  # the same snapshots to two independent receivers in one run.
  zfs create -p -o canmount=off $TESTFS/receiver/dest1
  zfs create -p -o canmount=off $TESTFS/receiver/dest2
  zfs create -p -o canmount=off $TESTFS/sender/mypool/myds
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote1
  cat >$TEST_TMP_DIR/sender_multidst.json <<EOF
{
  "include": ["$TESTFS/sender"],
  "sender": {
    "snapshot_re": "snap_remote.*",
    "destinations": [
      {
        "receiver": "$TEST_TMP_DIR/zfsbackup receiver --base_dataset=$TESTFS/receiver/dest1 -- ",
        "placeholders": ["dst1"]
      },
      {
        "receiver": "$TEST_TMP_DIR/zfsbackup receiver --base_dataset=$TESTFS/receiver/dest2 -- ",
        "placeholders": ["dst2"]
      }
    ]
  }
}
EOF
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_multidst.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver/dest1 -o name -H
  assert_output <<EOM
$TESTFS/receiver/dest1/$SENDERFS/mypool/myds@snap_remote1
EOM
  run zfs list -t snapshot -r $TESTFS/receiver/dest2 -o name -H
  assert_output <<EOM
$TESTFS/receiver/dest2/$SENDERFS/mypool/myds@snap_remote1
EOM
  # Both placeholder bookmarks should exist on the source.
  run zfs list -t bookmark $TESTFS/sender/mypool/myds -o name -H
  assert_output -e <<EOM
$SENDERFS/mypool/myds#snap_remote1-dst1
$SENDERFS/mypool/myds#snap_remote1-dst2
EOM
  # A second run is a no-op (nothing new to send).
  zfs snapshot $TESTFS/sender/mypool/myds@snap_remote2
  run $TEST_TMP_DIR/zfsbackup sender --config=$TEST_TMP_DIR/sender_multidst.json
  assert_success
  run zfs list -t snapshot -r $TESTFS/receiver/dest1 -o name -H
  assert_output <<EOM
$TESTFS/receiver/dest1/$SENDERFS/mypool/myds@snap_remote1
$TESTFS/receiver/dest1/$SENDERFS/mypool/myds@snap_remote2
EOM
  run zfs list -t snapshot -r $TESTFS/receiver/dest2 -o name -H
  assert_output <<EOM
$TESTFS/receiver/dest2/$SENDERFS/mypool/myds@snap_remote1
$TESTFS/receiver/dest2/$SENDERFS/mypool/myds@snap_remote2
EOM
}

@test "Deletions" {
  cat >$TEST_TMP_DIR/deleter.json <<EOF
{
  "include": ["$TESTFS/sender/mypool"],
  "exclude": ["$TESTFS/sender/mypool/preserve"],
  "deleter": {
    "regex": ["^snap-.*\$"],
    "preserve_top_n": 3,
    "preserve_newer_than": "3d",
    "rules": [
      {"interval": "6d", "count": 3},
      {"interval": "2d", "count": 3}
    ]
  }
}
EOF
  times=(
  "2022-06-11 02:00:00"
  "2022-09-24 02:00:00"
  "2022-09-25 02:00:00"
  "2022-09-26 02:00:00"
  "2022-09-27 02:00:00"
  "2022-09-28 02:00:00"
  "2022-09-29 02:00:00"
  "2022-09-30 02:00:00"
  "2022-10-01 02:00:00"
  "2022-10-02 02:00:00"
  "2022-10-03 02:00:00"
  "2022-10-04 02:00:00"
  "2022-10-05 02:00:00"
  "2022-10-06 02:00:00"
  "2022-10-07 02:00:00"
  "2022-10-07 14:00:00"
  "2022-10-08 02:00:00"
  "2022-10-08 14:00:00"
  )
  make_snaps $TESTFS/sender/mypool/fs_many_snaps "${times[@]}"
  run $TEST_TMP_DIR/zfsbackup deleter --dry-run=false --config=$TEST_TMP_DIR/deleter.json
  assert_success
  run zfs list -H -p -t snapshot $TESTFS/sender/mypool/fs_many_snaps -o name -H
  assert_output -e <<EOM
$TESTFS/sender/mypool/fs_many_snaps@snap-2-2022_09_24_02_00_00
$TESTFS/sender/mypool/fs_many_snaps@snap-3-2022_09_25_02_00_00
$TESTFS/sender/mypool/fs_many_snaps@snap-9-2022_10_01_02_00_00
$TESTFS/sender/mypool/fs_many_snaps@snap-11-2022_10_03_02_00_00
$TESTFS/sender/mypool/fs_many_snaps@snap-13-2022_10_05_02_00_00
$TESTFS/sender/mypool/fs_many_snaps@snap-14-2022_10_06_02_00_00
$TESTFS/sender/mypool/fs_many_snaps@snap-15-2022_10_07_02_00_00
$TESTFS/sender/mypool/fs_many_snaps@snap-16-2022_10_07_14_00_00
$TESTFS/sender/mypool/fs_many_snaps@snap-17-2022_10_08_02_00_00
$TESTFS/sender/mypool/fs_many_snaps@snap-18-2022_10_08_14_00_00
EOM
}

@test "Deletions_OneRule" {
  cat >$TEST_TMP_DIR/deleter.json <<EOF
{
  "include": ["$TESTFS/sender/mypool"],
  "exclude": ["$TESTFS/sender/mypool/preserve"],
  "deleter": {
    "regex": ["^snap-.*\$"],
    "rules": [
      {"interval": "4d", "count": 3}
    ]
  }
}
EOF
  times=(
  "2022-06-11 02:00:00"
  "2022-09-24 02:00:00"
  "2022-09-25 02:00:00"
  "2022-09-26 02:00:00"
  "2022-09-27 02:00:00"
  "2022-09-28 02:00:00"
  "2022-09-29 02:00:00"
  "2022-09-30 02:00:00"
  "2022-10-01 02:00:00"
  "2022-10-02 02:00:00"
  "2022-10-03 02:00:00"
  "2022-10-04 02:00:00"
  "2022-10-05 02:00:00"
  "2022-10-06 02:00:00"
  "2022-10-07 02:00:00"
  "2022-10-07 14:00:00"
  "2022-10-08 02:00:00"
  "2022-10-08 14:00:00"
  )
  make_snaps $TESTFS/sender/mypool/fs_many_snaps "${times[@]}"
  run $TEST_TMP_DIR/zfsbackup deleter --dry-run=false --config=$TEST_TMP_DIR/deleter.json
  assert_success
  run zfs list -H -p -t snapshot $TESTFS/sender/mypool/fs_many_snaps -o name -H
  assert_output -e <<EOM
$TESTFS/sender/mypool/fs_many_snaps@snap-3-2022_09_25_02_00_00
$TESTFS/sender/mypool/fs_many_snaps@snap-7-2022_09_29_02_00_00
$TESTFS/sender/mypool/fs_many_snaps@snap-11-2022_10_03_02_00_00
$TESTFS/sender/mypool/fs_many_snaps@snap-15-2022_10_07_02_00_00
EOM
}

@test "DeletionsWithHoles" {
  cat >$TEST_TMP_DIR/deleter.json <<EOF
{
  "include": ["$TESTFS/sender/mypool"],
  "deleter": {
    "regex": ["^.*\$"],
    "rules": [
      {"interval": "4d", "count": 10, "allow_holes": false}
    ]
  }
}
EOF
  times=(
  "30 days ago"
  "18 days ago"
  "2 days ago"
  )
  make_snaps $TESTFS/sender/mypool/fs_many_snaps "${times[@]}"
  run $TEST_TMP_DIR/zfsbackup deleter --dry-run=false --config=$TEST_TMP_DIR/deleter.json
  assert_failure
  assert_output -e <<EOM
hole in retention rule.*4d.*intervals
EOM
  run zfs list -H -p -t snapshot $TESTFS/sender/mypool/fs_many_snaps -o name -H
  assert_output -e <<EOM
$TESTFS/sender/mypool/fs_many_snaps@snap-1-30_days_ago
$TESTFS/sender/mypool/fs_many_snaps@snap-2-18_days_ago
$TESTFS/sender/mypool/fs_many_snaps@snap-3-2_days_ago
EOM
  # Now allow holes.
  cat >$TEST_TMP_DIR/deleter.json <<EOF
{
  "include": ["$TESTFS/sender/mypool"],
  "deleter": {
    "regex": ["^.*\$"],
    "rules": [
      {"interval": "7d", "count": 3, "allow_holes": true}
    ]
  }
}
EOF
  run $TEST_TMP_DIR/zfsbackup deleter --dry-run=false --config=$TEST_TMP_DIR/deleter.json
  assert_success
  run zfs list -H -p -t snapshot $TESTFS/sender/mypool/fs_many_snaps -o name -H
  assert_output -e <<EOM
$TESTFS/sender/mypool/fs_many_snaps@snap-2-18_days_ago
$TESTFS/sender/mypool/fs_many_snaps@snap-3-2_days_ago
EOM
}

function MonitorSetup(){
  cd $TEST_TMP_DIR
  times=(
  "60 days ago"
  "5 days ago"
  "4 days ago"
  )
  make_snaps $TESTFS/sender/mypool/fs1 "${times[@]}"
  make_snaps $TESTFS/sender/mypool/skipthis "${times[@]}"
  times=(
  "60 days ago"
  "5 days ago"
  "2 days ago"
  )
  make_snaps $TESTFS/sender/mypool/fs2 "${times[@]}"
  make_snaps $TESTFS/sender/mypool/fs2/sub "${times[@]}"
}

@test "MonitorPrometheus" {
  MonitorSetup
  run $TEST_TMP_DIR/zfsbackup monitor --config=$TEST_TMP_DIR/monitor.json
  assert_success
  run cat metrics.test
  assert_success
  POOL=$(echo $TESTFS|cut -f 1 -d /)
  POOLFREE=$(/sbin/zpool list -H -p -o capacity $POOL)
  assert_output -e <<EOM
PoolUsedSpacePercent\{pool="$POOL"\} $POOLFREE
EOM
  assert_output -e <<EOM
# HELP HasBrokenPool zfsbackup metric
# TYPE HasBrokenPool untyped
HasBrokenPool 0
# HELP LastSnapAge zfsbackup metric
# TYPE LastSnapAge untyped
LastSnapAge\{fs="$TESTFS/sender/mypool/fs1"\} 34560.
# HELP LastSnapTimestamp zfsbackup metric
# TYPE LastSnapTimestamp untyped
LastSnapTimestamp\{fs="$TESTFS/sender/mypool/fs1"\} 1[0-9]{9}
LastSnapAge\{fs="$TESTFS/sender/mypool/fs2"\} 17280.
LastSnapTimestamp\{fs="$TESTFS/sender/mypool/fs2"\} 1[0-9]{9}
LastSnapAge\{fs="$TESTFS/sender/mypool/fs2/sub"\} 17280.
LastSnapTimestamp\{fs="$TESTFS/sender/mypool/fs2/sub"\} 1[0-9]{9}

EOM
}
