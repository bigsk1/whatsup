# Install WhatsUp on Arch Linux or Omarchy

These steps install the Linux release in your user account and add WhatsUp to
your application menu. Use an x86-64 desktop with an unlocked Secret Service
keyring, such as GNOME Keyring.

## 1. Install the runtime

Install WebKitGTK 6, the archive tools and the media plugins calls use:

```bash
sudo pacman -Syu --needed webkitgtk-6.0 libarchive gst-plugins-base gst-plugins-good gst-plugin-pipewire
```

On Omarchy, use its own tools instead; a direct system upgrade with pacman is
blocked there:

```bash
omarchy update
omarchy pkg add webkitgtk-6.0 libarchive gst-plugins-base gst-plugins-good gst-plugin-pipewire
```

WebKitGTK brings in GTK 4 and JavaScriptCore. Go and Node.js are not needed.
`gst-plugins-base` provides the audio codec for voice calls; without it WhatsUp
cannot run call audio. Sending video in a call needs WebKitGTK 2.54.3 or newer:
check with `pacman -Q webkitgtk-6.0`. Omarchy's stable channel may carry an
earlier version for a while; see [Linux support](linux-support.md#calls) for
what happens then.
If your desktop needs a keyring setup, follow the
[GNOME Keyring instructions](https://wiki.archlinux.org/title/GNOME/Keyring).

## 2. Download WhatsUp

Download `whatsup_0.2.1_amd64.deb` and `SHA256SUMS` from the
[v0.2.1 release](https://github.com/bigsk1/whatsup/releases/tag/v0.2.1).
Save both files in `~/Downloads`, then check the download:

```bash
cd "$HOME/Downloads"
sha256sum --ignore-missing --check SHA256SUMS
```

Confirm that `whatsup_0.2.1_amd64.deb` reports `OK`.

## 3. Install the app and menu entry

If WhatsUp is already running, choose **Quit** from its tray menu first.
Run this block in Bash as your normal desktop user. It extracts the program
directly into `~/.local/opt/whatsup`, installs the app icon and license notices,
and creates the menu entry.

```bash
(
  set -euo pipefail
  package="$HOME/Downloads/whatsup_0.2.1_amd64.deb"
  appdir="$HOME/.local/opt/whatsup"
  data_home="${XDG_DATA_HOME:-$HOME/.local/share}"
  applications="$data_home/applications"
  icons="$data_home/icons/hicolor/scalable/apps"
  notices="$data_home/doc/whatsup"
  install -d "$appdir" "$applications" "$icons" "$notices"

  bsdtar -xOf "$package" data.tar.gz |
    bsdtar -xf - -C "$appdir" --strip-components=3 \
      ./opt/whatsup-development/whatsup \
      ./opt/whatsup-development/whatsup-webview-check

  bsdtar -xOf "$package" data.tar.gz |
    bsdtar -xOf - ./usr/share/icons/hicolor/scalable/apps/whatsup.svg \
      > "$icons/whatsup.svg"

  bsdtar -xOf "$package" data.tar.gz |
    bsdtar -xf - -C "$notices" --strip-components=5 \
      ./usr/share/doc/whatsup

  chmod 755 "$appdir/whatsup" "$appdir/whatsup-webview-check"
  chmod 644 "$icons/whatsup.svg"

  # Keep a software-rendering choice saved in an earlier menu entry, then
  # retire the names earlier versions used so only one entry remains.
  entry="$applications/io.github.bigsk1.whatsup.desktop"
  rendering=""
  for earlier in "$entry" "$applications/org.wails.whatsup.desktop" \
    "$applications/whatsup.desktop" "$applications/whatsup-development.desktop"; do
    if [ -f "$earlier" ] && grep -q '^Exec=.*--software-rendering' "$earlier"; then
      rendering=" --software-rendering"
    fi
  done
  rm -f "$applications/org.wails.whatsup.desktop" \
    "$applications/whatsup.desktop" "$applications/whatsup-development.desktop"

  cat > "$entry" <<EOF
[Desktop Entry]
Version=1.0
Type=Application
Name=WhatsUp
Comment=Private one-to-one desktop messenger
Exec="$appdir/whatsup"$rendering
Icon=whatsup
Terminal=false
Categories=Network;InstantMessaging;
StartupWMClass=io.github.bigsk1.whatsup
EOF
  chmod 644 "$entry"
)
```

The block looks for earlier WhatsUp menu entries only in your user data
directory (`$XDG_DATA_HOME/applications`, normally
`~/.local/share/applications`). If you keep launchers somewhere else, remove an
old `whatsup.desktop` or `org.wails.whatsup.desktop` there by hand, or the
desktop may show two WhatsUp entries.

Open your application menu and launch **WhatsUp**. No terminal needs to remain
open. Follow [Pair with someone](../README.md#pair-with-someone) to connect.

## Graphics and video

If the window renders incorrectly, quit WhatsUp and try software rendering:

```bash
"$HOME/.local/opt/whatsup/whatsup" --software-rendering
```

To keep that setting for menu launches, add `--software-rendering` after the
quoted executable path on the `Exec=` line in your
`io.github.bigsk1.whatsup.desktop` file.

For more video file formats in conversations, install:

```bash
sudo pacman -Syu --needed gst-plugins-bad gst-libav
```

On Omarchy: `omarchy pkg add gst-plugins-bad gst-libav`.

## Updating or removing WhatsUp

To update, quit WhatsUp, download and verify the new release, then repeat the
installation block with its filename. Keep the same installation directory.
Your identity, contacts and conversations are stored separately from the program.

To remove the app, turn off **Start at login** if enabled, quit WhatsUp, and
remove `~/.local/opt/whatsup`, the `io.github.bigsk1.whatsup.desktop` menu entry, the installed
`whatsup.svg` icon and `doc/whatsup` notices from your user data directory.
The default user data directory is `~/.local/share`.

Keep `~/.local/share/whatsup` and your desktop keyring to retain your profile and
conversations. See [Linux support](linux-support.md#local-data) for custom data
locations.
