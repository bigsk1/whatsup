# Install WhatsUp on Arch Linux or Omarchy

These steps install the Linux release in your user account and add WhatsUp to
your application menu. Use an x86-64 desktop with an unlocked Secret Service
keyring, such as GNOME Keyring.

## 1. Install the runtime

Install WebKitGTK 6 and the archive tools:

```bash
sudo pacman -Syu --needed webkitgtk-6.0 libarchive
```

WebKitGTK brings in GTK 4 and JavaScriptCore. Go and Node.js are not needed.
If your desktop needs a keyring setup, follow the
[GNOME Keyring instructions](https://wiki.archlinux.org/title/GNOME/Keyring).

## 2. Download WhatsUp

Download `whatsup_0.1.2_amd64.deb` and `SHA256SUMS` from the
[v0.1.2 release](https://github.com/bigsk1/whatsup/releases/tag/v0.1.2).
Save both files in `~/Downloads`, then check the download:

```bash
cd "$HOME/Downloads"
sha256sum --ignore-missing --check SHA256SUMS
```

Confirm that `whatsup_0.1.2_amd64.deb` reports `OK`.

## 3. Install the app and menu entry

If WhatsUp is already running, choose **Quit** from its tray menu first.
Run this block in Bash as your normal desktop user. It extracts the program
directly into `~/.local/opt/whatsup`, installs the app icon and license notices,
and creates the menu entry.

```bash
(
  set -euo pipefail
  package="$HOME/Downloads/whatsup_0.1.2_amd64.deb"
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

  cat > "$applications/whatsup.desktop" <<EOF
[Desktop Entry]
Version=1.0
Type=Application
Name=WhatsUp
Comment=Private one-to-one desktop messenger
Exec="$appdir/whatsup"
Icon=whatsup
Terminal=false
Categories=Network;InstantMessaging;
StartupWMClass=whatsup
EOF
  chmod 644 "$applications/whatsup.desktop"
)
```

Open your application menu and launch **WhatsUp**. No terminal needs to remain
open. Follow [Pair with someone](../README.md#pair-with-someone) to connect.

## Graphics and video

If the window renders incorrectly, quit WhatsUp and try software rendering:

```bash
"$HOME/.local/opt/whatsup/whatsup" --software-rendering
```

To keep that setting for menu launches, add `--software-rendering` after the
quoted executable path on the `Exec=` line in your `whatsup.desktop` file.

For additional video codecs, install:

```bash
sudo pacman -Syu --needed gst-plugins-good gst-plugins-bad gst-libav
```

## Updating or removing WhatsUp

To update, quit WhatsUp, download and verify the new release, then repeat the
installation block with its filename. Keep the same installation directory.
Your identity, contacts and conversations are stored separately from the program.

To remove the app, turn off **Start at login** if enabled, quit WhatsUp, and
remove `~/.local/opt/whatsup`, the `whatsup.desktop` menu entry, the installed
`whatsup.svg` icon and `doc/whatsup` notices from your user data directory.
The default user data directory is `~/.local/share`.

Keep `~/.local/share/whatsup` and your desktop keyring to retain your profile and
conversations. See [Linux support](linux-support.md#local-data) for custom data
locations.
