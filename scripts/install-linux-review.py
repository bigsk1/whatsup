#!/usr/bin/env python3
"""Install a private Linux review candidate; never execute either ELF as root.

This is a development integration install, not release packaging. It never
changes sysctls, disables WebKit's sandbox, installs packages, or touches app
data, credentials, user homes, launchers, or earlier immutable version folders.
The caller must verify and trust this installer and bundle before invoking sudo.
"""

from __future__ import annotations

import sys

# Do not import Python modules from a user-writable bundle directory or an
# inherited Conda/PYTHONPATH environment before a privileged installation.
if __name__ == "__main__" and not sys.flags.isolated:
    sys.stderr.write('Use the system Python in isolated mode: python3 -I -B install-linux-review.py ...\n')
    raise SystemExit(2)

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import stat
import subprocess
import tempfile

APP_ROOT = Path("/opt/whatsup-development")
POLICY_PATH = Path("/etc/apparmor.d/whatsup-development")
RESTRICT_PATH = Path("/proc/sys/kernel/apparmor_restrict_unprivileged_userns")
MAX_BINARY = 128 * 1024 * 1024
MAX_METADATA = 256 * 1024
PROFILE_TEXT = """# WhatsUp development user-namespace compatibility policy.
# This enables WebKit's own sandbox; it is not full confinement of the Go app.
abi <abi/4.0>,
include <tunables/global>

profile whatsup-development /opt/whatsup-development/whatsup flags=(unconfined) {
    userns,
}

profile whatsup-development-webview-check /opt/whatsup-development/whatsup-webview-check flags=(unconfined) {
    userns,
}
"""


class InstallError(Exception):
    """A bounded operator-facing validation failure."""


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def read_regular(path: Path, maximum: int) -> bytes:
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or path.is_symlink() or not 0 < info.st_size <= maximum:
        raise InstallError("Bundle files must be bounded nonempty regular files, without symlinks")
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_BINARY", 0)
    descriptor = os.open(path, flags)
    try:
        before = os.fstat(descriptor)
        if (before.st_dev, before.st_ino, before.st_size) != (info.st_dev, info.st_ino, info.st_size):
            raise InstallError("Bundle file changed during validation")
        chunks = []
        remaining = maximum + 1
        while remaining:
            chunk = os.read(descriptor, min(1024 * 1024, remaining))
            if not chunk:
                break
            chunks.append(chunk)
            remaining -= len(chunk)
        data = b"".join(chunks)
        after = os.fstat(descriptor)
        if len(data) != before.st_size or len(data) > maximum or (
            before.st_size, before.st_mtime_ns, before.st_ino
        ) != (after.st_size, after.st_mtime_ns, after.st_ino):
            raise InstallError("Bundle file changed during reading")
        return data
    finally:
        os.close(descriptor)


def strict_json(data: bytes) -> dict:
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise InstallError("Duplicate metadata fields are rejected")
            result[key] = value
        return result

    try:
        value = json.loads(data.decode("utf-8"), object_pairs_hook=pairs)
    except (UnicodeError, ValueError) as error:
        raise InstallError("Bundle metadata is invalid JSON") from error
    if not isinstance(value, dict):
        raise InstallError("Bundle metadata must be an object")
    return value


def exact_hash(value) -> bool:
    return isinstance(value, str) and re.fullmatch(r"[a-f0-9]{64}", value) is not None


def check_elf(data: bytes, architecture: str) -> None:
    machine = {"x64": 62, "arm64": 183}.get(architecture)
    if not machine or len(data) < 64 or data[:7] != b"\x7fELF\x02\x01\x01" or int.from_bytes(data[18:20], "little") != machine:
        raise InstallError("Executable must be a matching 64-bit little-endian Linux ELF")
    if int.from_bytes(data[16:18], "little") not in (2, 3):
        raise InstallError("ELF file must be an executable or position-independent executable")


def check_files_entry(manifest: dict, name: str, data: bytes) -> None:
    entries = manifest.get("files")
    if not isinstance(entries, list) or len(entries) != 1 or not isinstance(entries[0], dict):
        raise InstallError("Exactly one declared executable is required in each manifest")
    entry = entries[0]
    if entry.get("name") != name or type(entry.get("size")) is not int or entry["size"] != len(data) or entry.get("sha256") != sha256(data):
        raise InstallError("Executable does not match its declared size and SHA-256")


def check_bundle(directory: Path) -> dict:
    directory = directory.absolute()
    for candidate in [directory, *directory.parents]:
        info = candidate.lstat()
        if not stat.S_ISDIR(info.st_mode) or candidate.is_symlink():
            raise InstallError("Bundle path must not traverse symlinks")
    names = ("whatsup", "build-manifest.json", "SHA256SUMS", "whatsup-webview-check", "probe-manifest.json")
    data = {name: read_regular(directory / name, MAX_BINARY if name in ("whatsup", "whatsup-webview-check") else MAX_METADATA) for name in names}
    app = strict_json(data["build-manifest.json"])
    probe = strict_json(data["probe-manifest.json"])
    version = app.get("version")
    review_id = app.get("reviewID")
    source = app.get("source")
    if not isinstance(version, str) or re.fullmatch(r"(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)", version) is None:
        raise InstallError("Application version must be exact numeric metadata")
    if app.get("schemaVersion") != 1 or app.get("kind") != "development-review" or app.get("platform") != "linux" or app.get("arch") not in ("x64", "arm64"):
        raise InstallError("A supported Linux development review manifest is required")
    if app.get("developmentOnly") is not True or app.get("releaseEligible") is not False or app.get("signed") is not False:
        raise InstallError("Bundle must explicitly remain an unsigned development candidate")
    if not isinstance(source, dict) or not isinstance(source.get("commit"), str) or re.fullmatch(r"(?:[a-f0-9]{40}|[a-f0-9]{64})", source["commit"]) is None or type(source.get("dirty")) is not bool or not exact_hash(source.get("digestSHA256")):
        raise InstallError("Development source provenance is invalid")
    if not isinstance(review_id, str) or re.fullmatch(r"\d{8}T\d{9}Z-g[a-f0-9]{12}(?:-dirty)?", review_id) is None or not review_id.endswith("-g" + source["commit"][:12] + ("-dirty" if source["dirty"] else "")):
        raise InstallError("Review ID does not match its source provenance")
    if probe.get("schemaVersion") != 1 or probe.get("kind") != "linux-webview-check" or probe.get("version") != version or probe.get("reviewID") != review_id or probe.get("sourceDigestSHA256") != source["digestSHA256"]:
        raise InstallError("WebView probe must match the app version, build and source digest")
    check_elf(data["whatsup"], app["arch"])
    check_elf(data["whatsup-webview-check"], app["arch"])
    check_files_entry(app, "whatsup", data["whatsup"])
    check_files_entry(probe, "whatsup-webview-check", data["whatsup-webview-check"])
    try:
        checksum_lines = data["SHA256SUMS"].decode("ascii").splitlines()
    except UnicodeError as error:
        raise InstallError("Checksums must be ASCII") from error
    expected = {"whatsup": sha256(data["whatsup"]), "build-manifest.json": sha256(data["build-manifest.json"])}
    actual = {}
    for line in checksum_lines:
        match = re.fullmatch(r"([a-f0-9]{64})  (whatsup|build-manifest\.json)", line)
        if not match or match[2] in actual:
            raise InstallError("Checksums must name exactly the app and its manifest")
        actual[match[2]] = match[1]
    if actual != expected:
        raise InstallError("App or build manifest checksum differs")
    return {"version": version, "reviewID": review_id, "sourceDigestSHA256": source["digestSHA256"], "arch": app["arch"], "data": data, "_directory": directory}


def root_directory(directory: Path, create: bool = False) -> None:
    for candidate in reversed([directory, *directory.parents]):
        try:
            info = candidate.lstat()
        except FileNotFoundError:
            if not create:
                raise InstallError("Required root-owned installation directory is absent")
            candidate.mkdir(mode=0o755)
            candidate.chmod(0o755)
            info = candidate.lstat()
        if not stat.S_ISDIR(info.st_mode) or candidate.is_symlink() or info.st_uid != 0 or stat.S_IMODE(info.st_mode) & 0o022:
            raise InstallError("Installation parents must be root-owned, non-writable by others and not symlinks")
        if stat.S_IMODE(info.st_mode) & 0o005 != 0o005:
            raise InstallError("Installation parents must permit the desktop user to read and traverse them")


def existing_root_file(filename: Path, expected: bytes | None = None) -> None:
    try:
        info = filename.lstat()
    except FileNotFoundError:
        return
    if not stat.S_ISREG(info.st_mode) or filename.is_symlink() or info.st_uid != 0 or stat.S_IMODE(info.st_mode) & 0o022:
        raise InstallError("Existing installation file is unsafe")
    if expected is not None and read_regular(filename, MAX_METADATA) != expected:
        raise InstallError("Existing AppArmor policy differs; refusing to overwrite an administrator policy")


def atomic_root_file(filename: Path, data: bytes, mode: int) -> None:
    root_directory(filename.parent)
    existing_root_file(filename)
    descriptor, temporary = tempfile.mkstemp(prefix=".whatsup-install-", dir=filename.parent)
    try:
        os.fchmod(descriptor, mode)
        with os.fdopen(descriptor, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, filename)
        directory_fd = os.open(filename.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def userns_policy_required() -> bool:
    try:
        value = RESTRICT_PATH.read_text(encoding="ascii").strip()
    except FileNotFoundError:
        return False
    if value not in ("0", "1"):
        raise InstallError("Unknown AppArmor user-namespace policy value")
    return value == "1"


def parser_path() -> str:
    for candidate in ("/usr/sbin/apparmor_parser", "/sbin/apparmor_parser"):
        path = Path(candidate)
        if path.is_file() and not path.is_symlink():
            info = path.stat()
            if info.st_uid == 0 and not stat.S_IMODE(info.st_mode) & 0o022:
                return candidate
    raise InstallError("AppArmor parser is required for this host's namespace restriction")


def parse_policy(parser: str, load: bool) -> None:
    arguments = [parser, "--skip-cache", "--abort-on-error"]
    arguments.append("--replace" if load else "--skip-kernel-load")
    result = subprocess.run(arguments, input=PROFILE_TEXT.encode(), stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30, check=False)
    if result.returncode:
        raise InstallError("AppArmor policy could not be loaded" if load else "AppArmor ABI 4 policy validation failed")


def install_bundle(bundle: dict) -> dict:
    if sys.platform != "linux" or os.geteuid() != 0:
        raise InstallError("Installation requires root on Linux; use --check-bundle for a nonprivileged input check")
    if not sys.flags.isolated:
        raise InstallError("Privileged installation requires Python isolated mode")
    if {"x86_64": "x64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine()) != bundle["arch"]:
        raise InstallError("Bundle architecture does not match this Linux host")
    check_policy_template()
    required = userns_policy_required()
    parser = None
    if required:
        root_directory(POLICY_PATH.parent)
        existing_root_file(POLICY_PATH, PROFILE_TEXT.encode())
        parser = parser_path()
        parse_policy(parser, load=False)
    root_directory(APP_ROOT, create=True)
    root_directory(APP_ROOT / "versions", create=True)
    for name in ("whatsup", "whatsup-webview-check"):
        existing_root_file(APP_ROOT / name)
    destination = APP_ROOT / "versions" / bundle["reviewID"]
    reuse_archive = False
    try:
        destination.mkdir(mode=0o755)
        destination.chmod(0o755)
    except FileExistsError:
        root_directory(destination)
        if set(os.listdir(destination)) != set(bundle["data"]):
            raise InstallError("Existing review archive differs; it will not be overwritten")
        for name, data in bundle["data"].items():
            existing_root_file(destination / name)
            limit = MAX_BINARY if name in ("whatsup", "whatsup-webview-check") else MAX_METADATA
            if sha256(read_regular(destination / name, limit)) != sha256(data):
                raise InstallError("Existing review archive differs; it will not be overwritten")
        reuse_archive = True
    root_directory(destination)
    for name, data in ([] if reuse_archive else bundle["data"].items()):
        flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
        descriptor = os.open(destination / name, flags, 0o755 if name in ("whatsup", "whatsup-webview-check") else 0o644)
        os.fchmod(descriptor, 0o755 if name in ("whatsup", "whatsup-webview-check") else 0o644)
        with os.fdopen(descriptor, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        if sha256(read_regular(destination / name, MAX_BINARY if name in ("whatsup", "whatsup-webview-check") else MAX_METADATA)) != sha256(data):
            raise InstallError("Installed archive checksum differs")
    after = check_bundle(bundle["_directory"])
    if any(sha256(after["data"][name]) != sha256(data) for name, data in bundle["data"].items()):
        raise InstallError("Source bundle changed during installation")
    if required:
        atomic_root_file(POLICY_PATH, PROFILE_TEXT.encode(), 0o644)
        parse_policy(parser, load=True)
    for name in ("whatsup", "whatsup-webview-check"):
        data = bundle["data"][name]
        atomic_root_file(APP_ROOT / name, data, 0o755)
        if sha256(read_regular(APP_ROOT / name, MAX_BINARY)) != sha256(data):
            raise InstallError("Installed stable executable checksum differs")
    return {"version": bundle["version"], "reviewID": bundle["reviewID"], "sourceDigestSHA256": bundle["sourceDigestSHA256"], "developmentOnly": True, "releaseEligible": False,
            "appExecutable": str(APP_ROOT / "whatsup"), "probeExecutable": str(APP_ROOT / "whatsup-webview-check"), "archive": str(destination),
            "profileRequired": required, "profileLoaded": required, "reusedMatchingArchive": reuse_archive, "sysctlChanged": False, "userDataChanged": False, "executablesLaunchedAsRoot": False,
            "nextStep": "Run the installed probe as the desktop user before GUI handoff"}


def check_policy_template() -> None:
    policy_source = Path(__file__).absolute().parent.parent / "build/linux/apparmor/whatsup-development"
    if read_regular(policy_source, MAX_METADATA) != PROFILE_TEXT.encode():
        raise InstallError("Installer's scoped AppArmor template is missing or changed")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check-bundle", action="store_true", help="Validate bundle inputs without installing or changing policy")
    parser.add_argument("bundle", type=Path)
    arguments = parser.parse_args()
    try:
        bundle = check_bundle(arguments.bundle)
        check_policy_template()
        if arguments.check_bundle:
            result = {key: bundle[key] for key in ("version", "reviewID", "sourceDigestSHA256", "arch")}
            result.update({"bundleValid": True, "profileTemplateValid": True, "hostPolicyUnverified": True, "developmentOnly": True, "releaseEligible": False, "systemChanged": False})
        else:
            result = install_bundle(bundle)
        print(json.dumps(result, sort_keys=True))
        return 0
    except (InstallError, OSError, subprocess.TimeoutExpired) as error:
        message = str(error) if isinstance(error, InstallError) else "Filesystem or policy validation failed; no success is claimed"
        print(json.dumps({"installed": False, "error": message}), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
