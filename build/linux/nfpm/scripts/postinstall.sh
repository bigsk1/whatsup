#!/bin/sh
set -eu
# Keep the established attachment path. Never loosen the global namespace rule.
if [ -d /sys/kernel/security/apparmor ]; then
  /sbin/apparmor_parser -r /etc/apparmor.d/whatsup-development
fi
/usr/bin/python3 -I -B /opt/whatsup-development/migrate-launchers.py
if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database -q /usr/share/applications
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
  gtk-update-icon-cache -q -t -f /usr/share/icons/hicolor || true
fi
