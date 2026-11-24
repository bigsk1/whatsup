#!/usr/bin/env bash
# Source synchronization only. Build/test behavior stays in whatsup.mjs.
# Load the user's installed Go PATH without a login shell. Ubuntu's .bash_logout
# can fail clear_console under errexit when a noninteractive command has no TTY.
if [[ -r "$HOME/.profile" ]]; then . "$HOME/.profile"; fi
set -euo pipefail
source_dir=$1
workspace_id=$2
task=$3
shift 3
[[ $source_dir = /* && $workspace_id =~ ^[a-f0-9]{16}$ ]] || exit 2
managed_parent="$HOME/.cache/whatsup-development"
snapshot="$managed_parent/$workspace_id"
[[ ! -L "$HOME/.cache" && ! -L "$managed_parent" && ! -L "$snapshot" ]] || {
  echo 'Refusing a symlinked WSL snapshot directory.' >&2; exit 2;
}
mkdir -p -m 700 "$managed_parent" "$snapshot"
[[ $(realpath -- "$snapshot") = "$managed_parent/$workspace_id" ]] || exit 2
chmod 700 "$managed_parent" "$snapshot"
lock_file="$managed_parent/$workspace_id.lock"
[[ ! -L "$lock_file" && ( ! -e "$lock_file" || -f "$lock_file" ) ]] || exit 2
exec {snapshot_lock}>"$lock_file"
chmod 600 "$lock_file"
flock -n "$snapshot_lock" || {
  echo 'Another command is using this WSL workspace. Wait for it to finish.' >&2; exit 2;
}
exclude_file="$source_dir/scripts/wsl-excludes.txt"
# This derived snapshot is disposable; source edits belong in Windows. Keep
# platform dependencies and outputs local and exclude private application data.
rsync -a --delete --exclude-from="$exclude_file" "$source_dir/" "$snapshot/"
verify_snapshot() {
  local changes
  changes=$(rsync -rnc --delete --itemize-changes --exclude-from="$exclude_file" "$source_dir/" "$snapshot/")
  if [[ -n $changes ]]; then
    echo 'Source changed during synchronization or validation. Rerun from the Windows workspace.' >&2
    return 1
  fi
}
verify_snapshot
if [[ $task = build-review || $task = test-app || $task = check || $task = package ]]; then
  mkdir -p -m 700 "$snapshot/.local"
  [[ -f "$source_dir/.local/review-source.json" && ! -L "$source_dir/.local/review-source.json" ]] || exit 2
  cp -- "$source_dir/.local/review-source.json" "$snapshot/.local/review-source.json"
fi
export NVM_DIR="$HOME/.nvm"
. "$NVM_DIR/nvm.sh" --no-use
cd "$snapshot"
nvm use
# Windows npm exports these paths to child processes. Let the runner locate the
# selected Linux Node installation's own npm instead of inheriting a .js path on C:.
unset npm_execpath npm_node_execpath
export npm_execpath
npm_execpath=$(readlink -f "$(command -v npm)")
printf 'Derived WSL workspace: %s\n' "$snapshot"
set +e
node scripts/whatsup.mjs "$task" "$@"
result=$?
set -e
verify_snapshot
mkdir -p "$source_dir/.local"
for report in doctor-linux-x64.json validation-linux-x64.json review-build-linux-x64.json advisory-scan-linux-x64.json; do
  if [[ -f "$snapshot/.local/$report" && ! -L "$snapshot/.local/$report" ]]; then
    cp -- "$snapshot/.local/$report" "$source_dir/.local/$report"
  fi
done
printf 'Source equality verified before and after %s (command exit %s).\n' "$task" "$result"
exit "$result"
