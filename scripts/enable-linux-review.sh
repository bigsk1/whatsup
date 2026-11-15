#!/usr/bin/env bash
# Private development integration. Run as the desktop user; only installation
# invokes sudo. The WebKit probe and application are never launched as root.
set -euo pipefail
umask 077
[[ $(id -u) != 0 ]] || { echo 'Run this from your normal desktop terminal; the installer will ask for sudo.' >&2; exit 2; }
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
bundle="$script_dir/../bin"
force_software=false
launcher_options=()
for option in "$@"; do
  case "$option" in
    --software-rendering) force_software=true ;;
    --desktop-shortcut|--pin-to-dock) launcher_options+=("$option") ;;
    *) echo 'Accepted options: --software-rendering --desktop-shortcut --pin-to-dock.' >&2; exit 2 ;;
  esac
done
[[ -n ${DISPLAY:-} || -n ${WAYLAND_DISPLAY:-} ]] || { echo 'Run this command in your graphical desktop terminal.' >&2; exit 2; }
[[ ! -v WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS ]] || { echo 'Remove the dangerous WebKit sandbox override before testing.' >&2; exit 2; }
/usr/bin/python3 -I -B "$script_dir/install-linux-review.py" --check-bundle "$bundle"
# User-mounted FUSE/VMware shares may deliberately deny root access. Stage only
# the reviewed files onto the host's local temporary filesystem before sudo.
[[ -d /var/tmp && ! -L /var/tmp ]] || { echo 'A local /var/tmp directory is required for installation staging.' >&2; exit 2; }
stage_dir=$(mktemp -d /var/tmp/whatsup-linux-review.XXXXXX)
mkdir -p "$stage_dir/scripts" "$stage_dir/build/linux/apparmor" "$stage_dir/bin"
cp -- "$script_dir/install-linux-review.py" "$stage_dir/scripts/install-linux-review.py"
cp -- "$script_dir/install-linux-launcher.py" "$stage_dir/scripts/install-linux-launcher.py"
cp -- "$script_dir/../build/linux/apparmor/whatsup-development" "$stage_dir/build/linux/apparmor/whatsup-development"
cp -- "$script_dir/../build/appicon.png" "$stage_dir/build/appicon.png"
for name in whatsup build-manifest.json SHA256SUMS whatsup-webview-check probe-manifest.json; do
  cp -- "$bundle/$name" "$stage_dir/bin/$name"
done
/usr/bin/python3 -I -B "$stage_dir/scripts/install-linux-review.py" --check-bundle "$stage_dir/bin"
printf 'Private local staging: %s\n' "$stage_dir"
echo 'Installing root-owned review executables and the application-specific sandbox policy.'
/usr/bin/sudo -- /usr/bin/python3 -I -B "$stage_dir/scripts/install-linux-review.py" "$stage_dir/bin"
echo 'Testing a real sandboxed WebKit page in software mode (up to 25 seconds).'
/opt/whatsup-development/whatsup-webview-check
launch_options=''
if "$force_software"; then
  launch_options=' --software-rendering'
elif ! /opt/whatsup-development/whatsup-webview-check --accelerated; then
  echo 'Accelerated preflight did not pass; selecting supported software rendering.'
  launch_options=' --software-rendering'
fi
if [[ -n $launch_options ]]; then launcher_options+=(--software-rendering); fi
/usr/bin/python3 -I -B "$stage_dir/scripts/install-linux-launcher.py" "${launcher_options[@]}"
printf 'Setup and native runtime check passed. Launch WhatsUp from the application menu.\nRendering options:%s\n' "${launch_options:- accelerated}"
