#!/bin/sh
set -eu
# An upgrade's old postrm must not unload the replacement's policy.
case "${1:-}" in
  remove|purge)
    if [ -d /sys/kernel/security/apparmor ]; then
      for profile in whatsup-development whatsup-development-webview-check; do
        printf 'profile %s {}\n' "$profile" | /sbin/apparmor_parser -R || true
      done
    fi
    if command -v update-desktop-database >/dev/null 2>&1; then
      update-desktop-database -q /usr/share/applications
    fi
    ;;
esac
# Never delete homes, app data, Secret Service entries or review archives.
