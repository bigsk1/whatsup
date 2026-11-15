#!/usr/bin/env python3
"""Install user launchers for the reviewed native Linux app; never launch it."""

from __future__ import annotations

import sys

if __name__ == "__main__" and not sys.flags.isolated:
    sys.stderr.write("Use the system Python in isolated mode: python3 -I -B install-linux-launcher.py ...\n")
    raise SystemExit(2)

import argparse
import ast
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile

MAX_ICON_BYTES = 4 * 1024 * 1024


class LauncherError(Exception):
    """A bounded operator-facing failure without private path details."""


def no_symlink_components(path: Path) -> None:
    if not path.is_absolute() or ".." in path.parts:
        raise LauncherError("Launcher directories must use absolute paths without traversal")
    for component in [*reversed(path.parents), path]:
        try:
            info = component.lstat()
        except FileNotFoundError:
            continue
        if stat.S_ISLNK(info.st_mode):
            raise LauncherError("Launcher directories must not contain symlinks")
        if info.st_uid not in (0, os.getuid()):
            raise LauncherError("Launcher parent directories must not belong to another user")


def owned_directory(path: Path, *, create: bool = False) -> None:
    no_symlink_components(path)
    try:
        info = path.lstat()
    except FileNotFoundError:
        if not create:
            raise LauncherError("The selected launcher directory does not exist") from None
        owned_directory(path.parent, create=True)
        path.mkdir(mode=0o700)
        info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid():
        raise LauncherError("Launcher directories must be owned by the desktop user")
    if info.st_mode & 0o022:
        raise LauncherError("Launcher directories must not be writable by other users")


def regular_target(path: Path) -> None:
    try:
        info = path.lstat()
    except FileNotFoundError:
        return
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid():
        raise LauncherError("Existing launchers must be user-owned regular files without symlinks")


def command(arguments: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(arguments, capture_output=True, text=True, timeout=10, check=False)


def install_entry(target: Path, content: str, mode: int) -> None:
    owned_directory(target.parent)
    regular_target(target)
    descriptor, temporary = tempfile.mkstemp(prefix=".whatsup-", suffix=".desktop", dir=target.parent)
    temporary_path = Path(temporary)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="\n") as output:
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
            os.fchmod(output.fileno(), mode)
        validation = command(["/usr/bin/desktop-file-validate", str(temporary_path)])
        if validation.returncode:
            raise LauncherError("Desktop entry validation failed")
        regular_target(target)
        os.replace(temporary_path, target)
    finally:
        temporary_path.unlink(missing_ok=True)


def install_icon(data_directory: Path) -> tuple[Path, dict]:
    # This is the reviewed build asset, not a caller-selected image or path.
    source = Path(__file__).absolute().parent.parent / "build/appicon.png"
    no_symlink_components(source)
    info = source.lstat()
    if not stat.S_ISREG(info.st_mode) or not 33 <= info.st_size <= MAX_ICON_BYTES:
        raise LauncherError("The bundled application icon must be a bounded regular PNG")
    descriptor = os.open(source, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_CLOEXEC", 0))
    with os.fdopen(descriptor, "rb") as stream:
        before = os.fstat(stream.fileno())
        if (before.st_dev, before.st_ino, before.st_size) != (info.st_dev, info.st_ino, info.st_size):
            raise LauncherError("The bundled application icon changed while opening")
        content = stream.read(MAX_ICON_BYTES + 1)
        after = os.fstat(stream.fileno())
    if len(content) != before.st_size or len(content) > MAX_ICON_BYTES or (
        before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns
    ) != (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns):
        raise LauncherError("The bundled application icon changed while reading")
    if content[:16] != b"\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR" or content[-12:] != b"\x00\x00\x00\x00IEND\xaeB`\x82":
        raise LauncherError("The bundled application icon is not a complete PNG")
    width, height = int.from_bytes(content[16:20], "big"), int.from_bytes(content[20:24], "big")
    if not 0 < width <= 4096 or not 0 < height <= 4096:
        raise LauncherError("The bundled application icon dimensions exceed the supported bound")
    icons = data_directory / "icons"
    owned_directory(icons, create=True)
    target = icons / "whatsup-development.png"
    regular_target(target)
    descriptor, temporary = tempfile.mkstemp(prefix=".whatsup-icon-", suffix=".png", dir=icons)
    temporary_path = Path(temporary)
    try:
        with os.fdopen(descriptor, "wb") as output:
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
            os.fchmod(output.fileno(), 0o644)
        regular_target(target)
        os.replace(temporary_path, target)
        directory_fd = os.open(icons, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
        if target.read_bytes() != content:
            raise LauncherError("The installed application icon differs from the bundled bytes")
    finally:
        temporary_path.unlink(missing_ok=True)
    return target, {"path": str(target), "sha256": hashlib.sha256(content).hexdigest(),
                    "size": len(content), "width": width, "height": height}


def desktop_entry(software: bool, icon: Path, *, hidden: bool = False) -> str:
    options = " --software-rendering" if software else ""
    icon_value = str(icon).replace("\\", "\\\\").replace("\n", "\\n").replace("\r", "\\r").replace("\t", "\\t")
    return (
        "[Desktop Entry]\nVersion=1.0\nName=WhatsUp\n"
        "Comment=Private one-to-one desktop messenger\n"
        f"Exec=/opt/whatsup-development/whatsup{options}\n"
        f"TryExec=/opt/whatsup-development/whatsup\nIcon={icon_value}\n"
        "Terminal=false\nType=Application\nCategories=Network;InstantMessaging;\n"
        "StartupWMClass=whatsup\nX-WhatsUp-Package-Launcher=true\n"
        + ("NoDisplay=true\n" if hidden else "")
    )


def install_desktop_shortcut(content: str, result: dict) -> None:
    selected = command(["/usr/bin/xdg-user-dir", "DESKTOP"])
    directory = Path(selected.stdout.strip())
    if selected.returncode or not directory.is_absolute() or directory == Path.home():
        raise LauncherError("Desktop shortcuts are disabled or the desktop directory is unavailable")
    owned_directory(directory)
    target = directory / "whatsup.desktop"
    install_entry(target, content, 0o755)
    result["desktopShortcutInstalled"] = True
    try:
        trusted = command(["/usr/bin/gio", "set", str(target), "metadata::trusted", "true"])
        result["desktopShortcutTrusted"] = trusted.returncode == 0
    except (OSError, subprocess.SubprocessError):
        result["desktopShortcutTrusted"] = False
    if not result["desktopShortcutTrusted"]:
        result["warnings"].append("Desktop trust metadata unavailable; use Allow Launching in the desktop menu")


def pin_to_dock(result: dict) -> None:
    desktops = os.environ.get("XDG_CURRENT_DESKTOP", "").lower().split(":")
    if "gnome" not in desktops:
        raise LauncherError("Automatic dock pinning is supported only in a GNOME desktop session")
    base = ["/usr/bin/gsettings"]
    read = command([*base, "get", "org.gnome.shell", "favorite-apps"])
    if read.returncode or len(read.stdout) > 65536:
        raise LauncherError("GNOME dock preferences are unavailable")
    raw = read.stdout.strip()
    if raw.startswith("@as "):
        raw = raw[4:]
    try:
        favorites = ast.literal_eval(raw)
    except (SyntaxError, ValueError):
        raise LauncherError("GNOME dock preferences are not a string array") from None
    if not isinstance(favorites, list) or len(favorites) > 512 or any(
        not isinstance(value, str) or len(value) > 2048 or "\x00" in value for value in favorites
    ):
        raise LauncherError("GNOME dock preferences are not a bounded string array")
    updated = []
    for value in favorites:
        value = "whatsup.desktop" if value == "whatsup-development.desktop" else value
        if value != "whatsup.desktop" or value not in updated:
            updated.append(value)
    if "whatsup.desktop" not in updated:
        updated.append("whatsup.desktop")
    if updated != favorites:
        changed = command([*base, "set", "org.gnome.shell", "favorite-apps", repr(updated)])
        if changed.returncode:
            raise LauncherError("GNOME dock preferences could not be updated")
    result["dockPinned"] = True


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--software-rendering", action="store_true")
    parser.add_argument("--desktop-shortcut", action="store_true")
    parser.add_argument("--pin-to-dock", action="store_true")
    options = parser.parse_args()
    if sys.platform != "linux" or os.geteuid() == 0:
        raise LauncherError("Run this as the normal Linux desktop user, never as root")
    os.umask(0o077)
    home = Path.home()
    owned_directory(home)
    if os.environ.get("XDG_DATA_HOME"):
        data = Path(os.environ["XDG_DATA_HOME"])
        owned_directory(data, create=True)
    else:
        owned_directory(home / ".local", create=True)
        data = home / ".local/share"
        owned_directory(data, create=True)
    applications = data / "applications"
    owned_directory(applications, create=True)
    result = {"menuLauncherInstalled": False, "compatibilityAliasInstalled": False,
              "desktopShortcutInstalled": False, "desktopShortcutTrusted": False,
              "dockPinned": False, "appLaunched": False, "rootChanged": False, "warnings": []}
    icon, result["icon"] = install_icon(data)
    content = desktop_entry(options.software_rendering, icon)
    install_entry(applications / "whatsup.desktop", content, 0o644)
    result["menuLauncherInstalled"] = True
    legacy = applications / "whatsup-development.desktop"
    regular_target(legacy)
    legacy.unlink(missing_ok=True)
    result["compatibilityAliasRemoved"] = True
    for enabled, operation in [(options.desktop_shortcut, lambda: install_desktop_shortcut(content, result)),
                               (options.pin_to_dock, lambda: pin_to_dock(result))]:
        if enabled:
            try:
                operation()
            except LauncherError as error:
                result["warnings"].append(str(error))
            except (OSError, subprocess.SubprocessError):
                result["warnings"].append("Optional desktop integration is unavailable; the application-menu launcher is installed")
    try:
        refreshed = command(["/usr/bin/update-desktop-database", str(applications)])
        if refreshed.returncode:
            result["warnings"].append("Desktop menu cache refresh was unavailable")
    except (OSError, subprocess.SubprocessError):
        result["warnings"].append("Desktop menu cache refresh was unavailable")
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except LauncherError as error:
        sys.stderr.write(f"Launcher installation refused: {error}\n")
        raise SystemExit(2)
    except (OSError, subprocess.SubprocessError):
        sys.stderr.write("Launcher installation failed; check user directory access and desktop tools\n")
        raise SystemExit(2)
