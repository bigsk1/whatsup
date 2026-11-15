import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, readFile, readdir, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const installer = path.join(root, 'scripts/install-linux-review.py');
const launcher = path.join(root, 'scripts/install-linux-launcher.py');
const python = process.platform === 'win32' ? 'python' : 'python3';
const pythonAvailable = spawnSync(python, ['--version'], { windowsHide: true, stdio: 'ignore', timeout: 10000 }).status === 0;
const options = { skip: pythonAvailable ? false : 'Python is only required for Linux installation checks' };
const launcherOptions = { skip: process.platform === 'linux' ? options.skip : 'Linux user-owned launcher/icon integration' };
const hash = (bytes) => createHash('sha256').update(bytes).digest('hex');

async function bundleFixture(t) {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'whatsup-linux-install-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const elf = Buffer.alloc(80);
  Buffer.from([0x7f, 0x45, 0x4c, 0x46, 2, 1, 1]).copy(elf);
  elf.writeUInt16LE(2, 16);
  elf.writeUInt16LE(62, 18);
  const manifest = {
    schemaVersion: 1, kind: 'development-review', version: '0.0.2',
    reviewID: '20261114T070809123Z-gaaaaaaaaaaaa-dirty', platform: 'linux', arch: 'x64',
    developmentOnly: true, releaseEligible: false, signed: false,
    source: { commit: 'a'.repeat(40), dirty: true, digestSHA256: 'b'.repeat(64) },
    files: [{ name: 'whatsup', size: elf.length, sha256: hash(elf) }],
  };
  const probe = {
    schemaVersion: 1, kind: 'linux-webview-check', version: manifest.version,
    reviewID: manifest.reviewID, sourceDigestSHA256: manifest.source.digestSHA256,
    files: [{ name: 'whatsup-webview-check', size: elf.length, sha256: hash(elf) }],
  };
  const manifestBytes = JSON.stringify(manifest) + '\n';
  await writeFile(path.join(directory, 'whatsup'), elf);
  await writeFile(path.join(directory, 'whatsup-webview-check'), elf);
  await writeFile(path.join(directory, 'build-manifest.json'), manifestBytes);
  await writeFile(path.join(directory, 'probe-manifest.json'), JSON.stringify(probe) + '\n');
  await writeFile(path.join(directory, 'SHA256SUMS'), `${hash(elf)}  whatsup\n${hash(manifestBytes)}  build-manifest.json\n`);
  return { directory, manifest, probe, elf };
}

function check(directory) {
  return spawnSync(python, ['-I', '-B', installer, '--check-bundle', directory], { cwd: root, windowsHide: true, encoding: 'utf8', timeout: 10000 });
}

test('Linux private installer validates matching app/probe bytes without privileged side effects', options, async (t) => {
  const { directory } = await bundleFixture(t);
  const before = (await readdir(directory)).sort();
  const result = check(directory);
  assert.equal(result.status, 0, result.stderr);
  const summary = JSON.parse(result.stdout);
  assert.equal(summary.bundleValid, true);
  assert.equal(summary.profileTemplateValid, true);
  assert.equal(summary.hostPolicyUnverified, true);
  assert.equal(summary.systemChanged, false);
  assert.equal(summary.releaseEligible, false);
  assert.deepEqual((await readdir(directory)).sort(), before);
});

test('Linux private installer rejects mixed builds, tampering and checksum path injection', options, async (t) => {
  const { directory, probe, elf } = await bundleFixture(t);
  await writeFile(path.join(directory, 'probe-manifest.json'), JSON.stringify({ ...probe, sourceDigestSHA256: 'c'.repeat(64) }));
  assert.match(check(directory).stderr, /must match/);
  await writeFile(path.join(directory, 'probe-manifest.json'), JSON.stringify(probe));
  await writeFile(path.join(directory, 'whatsup'), Buffer.concat([elf, Buffer.from('tampered')]));
  assert.match(check(directory).stderr, /size and SHA-256/);
  await writeFile(path.join(directory, 'whatsup'), elf);
  await writeFile(path.join(directory, 'SHA256SUMS'), `${hash(elf)}  ../../etc/sudoers\n`);
  assert.match(check(directory).stderr, /exactly the app/);
});

test('Linux installer refuses a source symlink and enforces root before installation work', options, async (t) => {
  const { directory } = await bundleFixture(t);
  const source = path.join(directory, 'whatsup');
  const original = await readFile(source);
  await rm(source);
  await writeFile(path.join(directory, 'actual-elf'), original);
  try {
    await symlink(path.join(directory, 'actual-elf'), source, 'file');
    assert.match(check(directory).stderr, /without symlinks/);
  } catch (error) {
    if (!['EPERM', 'EACCES'].includes(error.code)) throw error;
  }
  const code = `import importlib.util,sys\nfrom unittest.mock import patch\nspec=importlib.util.spec_from_file_location('installer',sys.argv[1])\nm=importlib.util.module_from_spec(spec)\nspec.loader.exec_module(m)\nwith patch.object(m.sys,'platform','linux'), patch.object(m.os,'geteuid',return_value=1000,create=True):\n try:\n  m.install_bundle({})\n except m.InstallError as e:\n  print(str(e))\n else:\n  raise RuntimeError('privilege boundary not enforced')\n`;
  const result = spawnSync(python, ['-I', '-B', '-c', code, installer], { cwd: root, windowsHide: true, encoding: 'utf8', timeout: 10000 });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /requires root/);
});

async function launcherFixture(t) {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'whatsup-launcher-icon-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  await mkdir(path.join(directory, 'build'), { mode: 0o700 });
  await mkdir(path.join(directory, 'scripts'), { mode: 0o700 });
  const icon = await readFile(path.join(root, 'build/appicon.png'));
  await writeFile(path.join(directory, 'build/appicon.png'), icon, { mode: 0o600 });
  return { directory, icon };
}

function checkLauncher(code, directory) {
  return spawnSync(python, ['-I', '-B', '-c', code, launcher, directory], {
    cwd: root, windowsHide: true, encoding: 'utf8', timeout: 10000,
  });
}

test('Linux review launchers install the exact new icon and retain rendering, menu, desktop and dock behavior', launcherOptions, async (t) => {
  const { directory, icon } = await launcherFixture(t);
  const code = `import importlib.util,json,os,stat,subprocess,sys
from pathlib import Path
from unittest.mock import patch
spec=importlib.util.spec_from_file_location('launcher',sys.argv[1])
m=importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
root=Path(sys.argv[2]); m.__file__=str(root/'scripts/install-linux-launcher.py')
home=root/'home'; home.mkdir(mode=0o700)
desktop=home/'Desktop'; desktop.mkdir(mode=0o700)
data=home/'data with spaces'
calls=[]
def command(args):
 calls.append(args)
 if args[:2]==['/usr/bin/xdg-user-dir','DESKTOP']: output=str(desktop)+'\\n'
 elif args[:2]==['/usr/bin/gsettings','get']: output="['org.gnome.Terminal.desktop', 'whatsup-development.desktop']\\n"
 else: output=''
 return subprocess.CompletedProcess(args,0,stdout=output,stderr='')
with patch.object(m,'command',side_effect=command), patch.object(m.Path,'home',return_value=home), patch.dict(m.os.environ,{'XDG_DATA_HOME':str(data),'XDG_CURRENT_DESKTOP':'GNOME'}), patch.object(m.sys,'argv',['install-linux-launcher.py','--software-rendering','--desktop-shortcut','--pin-to-dock']), patch.object(m.os,'geteuid',return_value=1000):
 assert m.main()==0
installed=data/'icons/whatsup-development.png'
assert installed.read_bytes()==(root/'build/appicon.png').read_bytes()
assert stat.S_IMODE(installed.stat().st_mode)==0o644 and installed.stat().st_uid==os.getuid()
for relative in ['applications/whatsup.desktop','applications/whatsup-development.desktop']:
 entry=(data/relative).read_text()
 assert 'Icon='+str(installed)+'\\n' in entry and 'internet-chat' not in entry
 assert 'Exec=/opt/whatsup-development/whatsup --software-rendering\\n' in entry
 assert 'StartupWMClass=whatsup\\n' in entry
assert 'NoDisplay=true\\n' in (data/'applications/whatsup-development.desktop').read_text()
assert (desktop/'whatsup.desktop').read_text()==(data/'applications/whatsup.desktop').read_text()
assert stat.S_IMODE((desktop/'whatsup.desktop').stat().st_mode)==0o755
assert ['/usr/bin/gsettings','set','org.gnome.shell','favorite-apps',"['org.gnome.Terminal.desktop', 'whatsup.desktop']"] in calls
assert all(call[0] in ['/usr/bin/desktop-file-validate','/usr/bin/xdg-user-dir','/usr/bin/gio','/usr/bin/gsettings','/usr/bin/update-desktop-database'] for call in calls)
`;
  const result = checkLauncher(code, directory);
  assert.equal(result.status, 0, result.stderr);
  const receipt = JSON.parse(result.stdout);
  assert.equal(receipt.icon.sha256, hash(icon));
  assert.equal(receipt.icon.size, icon.length);
  assert.equal(receipt.icon.width, icon.readUInt32BE(16));
  assert.equal(receipt.icon.height, icon.readUInt32BE(20));
  assert.equal(receipt.menuLauncherInstalled, true);
  assert.equal(receipt.desktopShortcutInstalled, true);
  assert.equal(receipt.dockPinned, true);
  assert.equal(receipt.appLaunched, false);
  assert.equal(receipt.rootChanged, false);
});

test('Linux icon installation rejects unsafe or malformed assets without replacing an existing icon', launcherOptions, async (t) => {
  const { directory } = await launcherFixture(t);
  const code = `import importlib.util,sys
from pathlib import Path
spec=importlib.util.spec_from_file_location('launcher',sys.argv[1])
m=importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
root=Path(sys.argv[2]); m.__file__=str(root/'scripts/install-linux-launcher.py')
data=root/'data'; data.mkdir(mode=0o700)
source=root/'build/appicon.png'; original=source.read_bytes()
target,_=m.install_icon(data)
def refused():
 try: m.install_icon(data)
 except m.LauncherError: pass
 else: raise AssertionError('unsafe image accepted')
 assert target.read_bytes()==original
for invalid in [b'',b'not a PNG'*8,original[:-12],b'x'*(m.MAX_ICON_BYTES+1)]:
 source.write_bytes(invalid); refused()
source.write_bytes(original)
other=root/'other.png'; other.write_bytes(original)
source.unlink(); source.symlink_to(other); refused()
source.unlink(); source.write_bytes(original)
target.unlink(); target.symlink_to(other)
try: m.install_icon(data)
except m.LauncherError: pass
else: raise AssertionError('existing icon symlink accepted')
assert target.is_symlink() and other.read_bytes()==original
assert not list((data/'icons').glob('.whatsup-icon-*'))
`;
  const result = checkLauncher(code, directory);
  assert.equal(result.status, 0, result.stderr);
});
