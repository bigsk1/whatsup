import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { within } from '../lib/workflow.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');

async function runRenderingRegression(t, body) {
  const directory = await mkdtemp(path.join(os.homedir(), '.whatsup-launcher-qa-'));
  t.after(async () => {
    assert.equal(within(os.homedir(), directory), true);
    assert.match(path.basename(directory), /^\.whatsup-launcher-qa-/);
    await rm(directory, { recursive: true, force: true });
  });
  const prelude = String.raw`import importlib.util, os, stat, sys
from pathlib import Path
spec=importlib.util.spec_from_file_location('migration',sys.argv[1]); m=importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
base=Path(sys.argv[2]); home=base/'home'; home.mkdir(mode=0o700)
apps=home/'.local/share/applications'; apps.mkdir(mode=0o700,parents=True)
desktop=home/'Desktop'; desktop.mkdir(mode=0o700)
uid=os.getuid()
`;
  const result = spawnSync('python3', ['-I', '-B', '-c', prelude + body, path.join(root, 'scripts/migrate-linux-launchers.py'), directory],
    { encoding: 'utf8', timeout: 20000 });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /rendering regression passed/);
}

test('Linux launcher migration preserves software rendering on Exec while TryExec remains the bare executable', { skip: process.platform !== 'linux' }, async (t) => {
  await runRenderingRegression(t, String.raw`legacy='[Desktop Entry]\nName=WhatsUp (Development)\nExec=/opt/whatsup-development/whatsup --software-rendering\nTryExec=/opt/whatsup-development/whatsup\n'
targets=[(apps/'whatsup.desktop',0o644),(desktop/'whatsup.desktop',0o755)]
for target,mode in targets:
 target.write_text(legacy); target.chmod(mode)
assert m.migrate_home(home,uid)==2
# The application-menu entry now carries the application ID as its name.
assert not (apps/'whatsup.desktop').exists()
targets=[(apps/'io.github.bigsk1.whatsup.desktop',0o644),(desktop/'whatsup.desktop',0o755)]
for target,mode in targets:
 lines=target.read_text().splitlines()
 assert [line for line in lines if line.startswith('Exec=')]==['Exec=/opt/whatsup-development/whatsup --software-rendering']
 assert [line for line in lines if line.startswith('TryExec=')]==['TryExec=/opt/whatsup-development/whatsup']
 assert stat.S_IMODE(target.stat().st_mode)==mode
 assert target.stat().st_uid==uid
print('legacy software rendering regression passed')
`);
});

test('Linux launcher migration repairs an already broken managed TryExec without dropping the Exec rendering preference', { skip: process.platform !== 'linux' }, async (t) => {
  await runRenderingRegression(t, String.raw`broken='[Desktop Entry]\nName=WhatsUp\nComment=Private one-to-one desktop messenger\nExec=/opt/whatsup-development/whatsup --software-rendering\nTryExec=/opt/whatsup-development/whatsup --software-rendering\nX-WhatsUp-Package-Launcher=true\n'
targets=[(apps/'whatsup.desktop',0o644),(desktop/'whatsup.desktop',0o755)]
for target,mode in targets:
 target.write_text(broken); target.chmod(mode)
assert m.migrate_home(home,uid)==2
# The application-menu entry now carries the application ID as its name.
assert not (apps/'whatsup.desktop').exists()
targets=[(apps/'io.github.bigsk1.whatsup.desktop',0o644),(desktop/'whatsup.desktop',0o755)]
for target,mode in targets:
 lines=target.read_text().splitlines()
 assert 'X-WhatsUp-Package-Launcher=true' in lines
 assert [line for line in lines if line.startswith('Exec=')]==['Exec=/opt/whatsup-development/whatsup --software-rendering']
 assert [line for line in lines if line.startswith('TryExec=')]==['TryExec=/opt/whatsup-development/whatsup']
 assert stat.S_IMODE(target.stat().st_mode)==mode
 assert target.stat().st_uid==uid
print('repaired managed software rendering regression passed')
`);
});

test('Linux legacy launcher migration preserves data and rejects swapped files, symlink parents and hardlinks', { skip: process.platform !== 'linux' }, async (t) => {
  const directory = await mkdtemp(path.join(os.homedir(), '.whatsup-launcher-qa-'));
  t.after(async () => {
    assert.equal(within(os.homedir(), directory), true);
    assert.match(path.basename(directory), /^\.whatsup-launcher-qa-/);
    await rm(directory, { recursive: true, force: true });
  });
  const code = String.raw`import importlib.util, os, sys
from pathlib import Path
from unittest.mock import patch
spec=importlib.util.spec_from_file_location('migration',sys.argv[1]); m=importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
base=Path(sys.argv[2]); home=base/'home'; home.mkdir(mode=0o700)
apps=home/'.local/share/applications'; apps.mkdir(mode=0o700,parents=True)
desktop=home/'Desktop'; desktop.mkdir(mode=0o700)
startup=home/'.config/autostart'; startup.mkdir(mode=0o700,parents=True)
uid=os.getuid()
old='[Desktop Entry]\nName=WhatsUp (Development)\nExec=/opt/whatsup-development/whatsup --software-rendering\n'.replace('\\n','\n')
for target in [apps/'whatsup.desktop',apps/'whatsup-development.desktop',desktop/'whatsup.desktop']: target.write_text(old)
profile=home/'.local/share/whatsup'; profile.mkdir(mode=0o700); vault=profile/'state.vault'; vault.write_bytes(b'synthetic encrypted-profile sentinel')
autostart=startup/'whatsup.desktop'; autostart.write_text('[Desktop Entry]\nX-WhatsUp-Managed=true\nExec="/opt/whatsup-development/whatsup" --autostart\n'.replace('\\n','\n'))
assert m.migrate_home(home,uid)==3
assert not (apps/'whatsup-development.desktop').exists()
# The menu entry is renamed to the application ID; its content is kept.
renamed=apps/'io.github.bigsk1.whatsup.desktop'
assert not (apps/'whatsup.desktop').exists()
assert 'Name=WhatsUp\n'.replace('\\n','\n') in renamed.read_text()
assert '--software-rendering' in renamed.read_text()
assert renamed.stat().st_nlink==1 and renamed.stat().st_uid==uid
assert m.migrate_home(home,uid+1)==0
# Replace a checked file just before the final inode comparison.
target=apps/'whatsup.desktop'; target.write_text(old)
original_stat=m.os.stat; replaced=False
def replace_at_stat(name,*args,**kwargs):
 global replaced
 if name=='whatsup.desktop' and 'dir_fd' in kwargs and not replaced:
  replaced=True; target.rename(apps/'old.desktop'); target.write_text('unrelated user replacement')
 return original_stat(name,*args,**kwargs)
parent=m.open_directory(apps,uid)
with patch.object(m.os,'stat',side_effect=replace_at_stat): assert not m.mutate_entry(parent,'whatsup.desktop',uid,remove=True)
os.close(parent)
assert target.read_text()=='unrelated user replacement'
# A symlink replacing the parent never redirects operations outside the held fd.
target.write_text(old); parent=m.open_directory(apps,uid); moved=apps.with_name('old-applications'); apps.rename(moved)
outside=base/'outside'; outside.mkdir(mode=0o700); other=outside/'whatsup.desktop'; other.write_text('keep outside')
apps.symlink_to(outside,target_is_directory=True)
assert m.mutate_entry(parent,'whatsup.desktop',uid,remove=True); os.close(parent)
assert other.read_text()=='keep outside'
assert m.migrate_home(home,uid)==1  # Only the safe Desktop entry is rewritten.
assert other.read_text()=='keep outside'
apps.unlink(); moved.rename(apps)
# Hardlinks are not edited even when their bytes resemble a known launcher.
target.write_text(old); hard=apps/'hardlink.desktop'; os.link(target,hard)
parent=m.open_directory(apps,uid); assert not m.mutate_entry(parent,'whatsup.desktop',uid,remove=False); os.close(parent)
hard.unlink(); target.write_text(old)
# An entry under the new name is already there: it is kept, not replaced, and
# the old name beside it is retired.
renamed.write_text('[Desktop Entry]\nName=WhatsUp\nExec=/opt/whatsup-development/whatsup\nX-WhatsUp-Package-Launcher=true\n'.replace('\\n','\n'))
assert m.migrate_home(home,uid)==3
assert not target.exists() and '--software-rendering' not in renamed.read_text()
# An entry of that name that is not WhatsUp's own is left alone, and the old
# name is still retired without replacing it.
renamed.write_text('unrelated entry'); target.write_text(old)
assert m.migrate_home(home,uid)==2
assert not target.exists() and renamed.read_text()=='unrelated entry'
renamed.unlink(); target.write_text(old)
assert m.migrate_home(home,uid)==2
# The entry name used briefly under the framework's namespace is moved too.
interim=apps/'org.wails.whatsup.desktop'
renamed.rename(interim)
assert m.migrate_home(home,uid)==2
assert not interim.exists() and 'X-WhatsUp-Package-Launcher=true' in renamed.read_text()
assert m.migrate_home(home,uid,uninstall=True)==3
assert not target.exists() and not renamed.exists() and not autostart.exists() and not (desktop/'whatsup.desktop').exists()
assert vault.read_bytes()==b'synthetic encrypted-profile sentinel'
print('launcher migration and uninstall isolation passed')
`;
  const result = spawnSync('python3', ['-I', '-B', '-c', code, path.join(root, 'scripts/migrate-linux-launchers.py'), directory],
    { encoding: 'utf8', timeout: 20000 });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /isolation passed/);
});
