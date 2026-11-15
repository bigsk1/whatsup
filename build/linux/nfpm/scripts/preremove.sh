#!/bin/sh
set -eu
if [ "${1:-}" = remove ] && command -v pgrep >/dev/null 2>&1 && pgrep -x whatsup >/dev/null 2>&1; then
  echo "Quit WhatsUp from its tray menu before uninstalling. Your profile and keys will be kept." >&2
  exit 1
fi
if [ "${1:-}" = remove ]; then
  /usr/bin/python3 -I -B /opt/whatsup-development/migrate-launchers.py --uninstall
fi
