#!/usr/bin/env python3
"""Retire only known review launchers; never touch application/profile data."""

from __future__ import annotations

import os
from pathlib import Path
import pwd
import stat
import sys

# The application-menu entry is named after the application ID: a desktop looks
# it up by that name when the app asks for the camera, and GNOME only asks when
# the application in front is the entry so named.
LAUNCHER = "io.github.bigsk1.whatsup.desktop"
LEGACY = ("whatsup.desktop", "whatsup-development.desktop", "org.wails.whatsup.desktop")

ENTRY = """[Desktop Entry]
Version=1.0
Name=WhatsUp
Comment=Private one-to-one desktop messenger
Exec=/opt/whatsup-development/whatsup
TryExec=/opt/whatsup-development/whatsup
Terminal=false
Type=Application
Icon=whatsup
Categories=Network;InstantMessaging;
StartupWMClass=io.github.bigsk1.whatsup
X-WhatsUp-Package-Launcher=true
"""


def checked_directory(parent: int | None, name: str, uid: int) -> int:
    descriptor = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
    info = os.fstat(descriptor)
    if not stat.S_ISDIR(info.st_mode) or info.st_uid not in (0, uid) or info.st_mode & 0o022:
        os.close(descriptor)
        raise OSError("Unsafe launcher directory")
    return descriptor


def open_directory(directory: Path, uid: int) -> int:
    if not directory.is_absolute() or ".." in directory.parts:
        raise OSError("Invalid launcher directory")
    descriptor = checked_directory(None, "/", uid)
    try:
        for part in directory.parts[1:]:
            child = checked_directory(descriptor, part, uid)
            os.close(descriptor)
            descriptor = child
        return descriptor
    except BaseException:
        os.close(descriptor)
        raise


def mutate_entry(parent: int, name: str, uid: int, *, remove: bool, autostart: bool = False,
                 rename_to: str | None = None) -> bool:
    if name not in (LAUNCHER, *LEGACY) or rename_to not in (None, LAUNCHER):
        raise ValueError("Only explicit WhatsUp launcher names are allowed")
    try:
        descriptor = os.open(name, (os.O_RDONLY if remove else os.O_RDWR) | os.O_NOFOLLOW, dir_fd=parent)
        with os.fdopen(descriptor, "r" if remove else "r+", encoding="utf-8", newline="\n") as stream:
            info = os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_uid != uid or info.st_size > 16384 or info.st_nlink != 1:
                return False
            content = stream.read(16385)
            lines = content.splitlines()
            if len(content) > 16384 or "[Desktop Entry]" not in lines:
                return False
            if autostart:
                managed = "X-WhatsUp-Managed=true" in lines
                valid_exec = any(line in (
                    'Exec="/opt/whatsup-development/whatsup" --autostart',
                    'Exec="/opt/whatsup-development/whatsup" --autostart --software-rendering',
                    'Exec="/usr/bin/whatsup" --autostart',
                    'Exec="/usr/bin/whatsup" --autostart --software-rendering',
                ) for line in lines)
            else:
                managed = any(line in lines for line in (
                    "Name=WhatsUp (Development)", "Comment=Private WhatsUp native startup and storage review",
                    "X-WhatsUp-Package-Launcher=true",
                ))
                valid_exec = any(line in ("Exec=/opt/whatsup-development/whatsup",
                    "Exec=/opt/whatsup-development/whatsup --software-rendering") for line in lines)
            if not managed or not valid_exec:
                return False
            # Work relative to the held parent, never re-resolve user-controlled
            # ancestors. Do not remove a replacement with a different inode.
            current = os.stat(name, dir_fd=parent, follow_symlinks=False)
            if (info.st_dev, info.st_ino, info.st_uid, info.st_size, info.st_mtime_ns) != (
                current.st_dev, current.st_ino, current.st_uid, current.st_size, current.st_mtime_ns):
                return False
            if remove:
                os.unlink(name, dir_fd=parent)
            else:
                replacement = ENTRY
                if "Exec=/opt/whatsup-development/whatsup --software-rendering" in lines:
                    replacement = replacement.replace("\nExec=/opt/whatsup-development/whatsup\n", "\nExec=/opt/whatsup-development/whatsup --software-rendering\n", 1)
                stream.seek(0)
                stream.truncate()
                stream.write(replacement)
                stream.flush()
                os.fsync(stream.fileno())
                if rename_to:
                    # Give the entry its new name without replacing one that
                    # is already there, then retire the old name.
                    try:
                        os.link(name, rename_to, src_dir_fd=parent, dst_dir_fd=parent, follow_symlinks=False)
                    except FileExistsError:
                        pass
                    os.unlink(name, dir_fd=parent)
            os.fsync(parent)
            return True
    except (OSError, UnicodeError):
        return False


def migrate_home(home: Path, uid: int, *, uninstall: bool = False) -> int:
    try:
        home_fd = open_directory(home, uid)
    except OSError:
        return 0
    changed = 0
    try:
        if os.fstat(home_fd).st_uid != uid:
            return 0
        scopes = [((".local", "share", "applications"), False), (("Desktop",), False)]
        if uninstall:
            scopes.append(((".config", "autostart"), True))
        for parts, autostart in scopes:
            descriptor = os.dup(home_fd)
            try:
                for part in parts:
                    child = checked_directory(descriptor, part, uid)
                    os.close(descriptor)
                    descriptor = child
                if parts[-1] == "applications":
                    # The current name first, so an entry already there is kept
                    # and the old name beside it is simply retired.
                    plan = ((LAUNCHER, uninstall, None),
                            ("whatsup.desktop", uninstall, None if uninstall else LAUNCHER),
                            ("org.wails.whatsup.desktop", uninstall, None if uninstall else LAUNCHER),
                            ("whatsup-development.desktop", True, None))
                else:
                    plan = (("whatsup.desktop", uninstall, None),)
                for name, remove, rename_to in plan:
                    changed += int(mutate_entry(descriptor, name, uid,
                        remove=remove, autostart=autostart, rename_to=rename_to))
            except OSError:
                pass
            finally:
                os.close(descriptor)
    finally:
        os.close(home_fd)
    return changed


def main() -> int:
    if sys.platform != "linux" or os.geteuid() != 0 or not sys.flags.isolated:
        raise RuntimeError("Launcher migration requires the isolated system Python under the package manager")
    changed = 0
    if sys.argv[1:] not in ([], ["--uninstall"]):
        raise RuntimeError("Unsupported package migration option")
    for account in pwd.getpwall():
        if account.pw_uid >= 1000 and account.pw_uid < 65534:
            changed += migrate_home(Path(account.pw_dir), account.pw_uid, uninstall=sys.argv[1:] == ["--uninstall"])
    print(f"Updated {changed} legacy WhatsUp launcher entries; profile data was not changed.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
