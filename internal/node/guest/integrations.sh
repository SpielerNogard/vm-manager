mkdir -p /etc/vmm
cat >/etc/vmm/gitconfig <<'VMM_EOF'
[credential "https://github.com"]
	helper = vmm
[url "https://github.com/"]
	insteadOf = git@github.com:
VMM_EOF

cat >/etc/systemd/system/vmm-integrations.service <<'VMM_EOF'
[Unit]
Description=vmm integrations sync
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/vmm-sync
TimeoutStartSec=4min
VMM_EOF
cat >/etc/systemd/system/vmm-integrations.timer <<'VMM_EOF'
[Unit]
Description=vmm integrations sync every 5 minutes

[Timer]
OnBootSec=30s
OnUnitActiveSec=5min

[Install]
WantedBy=timers.target
VMM_EOF
systemctl daemon-reload
systemctl enable --now --no-block vmm-integrations.timer
