Port 62122
ListenAddress 127.0.0.1

HostKey ${SSH_TEST_PATH}/ssh_host_ed25519_key
PidFile ${TEST_TMP_DIR}/sshd.pid

PubkeyAuthentication yes

AuthorizedKeysFile ${SSH_TEST_PATH}/authorized_keys

HostbasedAuthentication no
PasswordAuthentication no
ChallengeResponseAuthentication no
PermitRootLogin prohibit-password
StrictModes no

UsePAM no
UseDNS no

# Example of overriding settings on a per-user basis
#Match User anoncvs
#       X11Forwarding no
#       AllowTcpForwarding no
#       PermitTTY no
#       ForceCommand cvs server
