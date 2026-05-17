.PHONY: rekey start_server

ssh_host_ed25519_key: rekey
	true

authorized_keys: rekey
	true

rekey:
	rm ed25519_key ed25519_key.pub || /bin/true
	rm ssh_host_ed25519_key ssh_host_ed25519_key.pub  || /bin/true
	ssh-keygen -t ed25519 -f ed25519_key -N ""
	ssh-keygen -t ed25519 -f ssh_host_ed25519_key -N ""
	echo -n "[127.0.0.1]:62122 " >known_hosts
	cat ssh_host_ed25519_key.pub >>known_hosts
	echo -n 'command="REPLACEME" '>authorized_keys
	cat ed25519_key.pub >>authorized_keys

start_server: ssh_host_ed25519_key authorized_keys
	cat sshd_config.tpl |envsubst >sshd_config
	mkdir /run/sshd || true
	/usr/sbin/sshd -f sshd_config -E /tmp/sshd_log
