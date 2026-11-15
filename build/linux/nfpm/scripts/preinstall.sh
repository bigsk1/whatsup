#!/bin/sh
set -eu
for directory in /opt /opt/whatsup-development; do
  if [ -e "$directory" ] || [ -L "$directory" ]; then
    if [ -L "$directory" ] || [ ! -d "$directory" ] || [ "$(stat -c %u -- "$directory")" != 0 ]; then
      echo "WhatsUp requires a root-owned program directory without symlinks." >&2
      exit 1
    fi
    mode=$(stat -c %a -- "$directory")
    if [ "$((0$mode & 022))" -ne 0 ]; then
      echo "WhatsUp's program directory must not be writable by other users." >&2
      exit 1
    fi
  fi
done
# Upgrade program files only after the user has quit the running app.
if command -v pgrep >/dev/null 2>&1 && pgrep -x whatsup >/dev/null 2>&1; then
  echo "Quit WhatsUp from its tray menu before installing or upgrading. Your data is preserved." >&2
  exit 1
fi
