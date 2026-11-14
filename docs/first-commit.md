# First local source baseline

This baseline records the current private development foundation. It does not
claim a complete messenger, a green release gate, or permission to publish.
The owner recorded the reviewed foundation as local main-branch commit
`27e4c4898dbc4c2ac86c3590bec3a5bf09b27399`. No remote, push, tag, release or relay
deployment was created. The owner subsequently authorized a local checkpoint
for versioned review builds and the verified 0.0.2 native integration. These
notes preserve the scope of the original baseline.

## Included source

- The narrow desktop bridge and native shell, encrypted identity and coordinated
  state envelope, invitation/chat/transport domain components and their tests.
- Svelte frontend, delayed-runtime startup correction, generated public bindings,
  appearance settings, frontend unit tests and browser regressions.
- Exact npm/Go graphs, reviewed tool policy, the shared command runner, automatic
  WSL snapshot workflow, CI definitions and current documentation.
- Required native source/build assets, Wails provenance and its retained MIT notice.
  This does not select a public license for WhatsUp's original code.

Dependencies, CLI binaries, native candidates, frontend bundles, local validation
reports, private state, credential material and browser profiles are excluded.
`.gitattributes` pins text to LF without changing any global Git settings.

## Evidence and remaining limits

The last Windows and Linux component runs passed functional checks: fourteen
workflow tests, seven frontend unit tests, eleven Chromium tests, Go vet and nine
Go component/integration packages. Linux used the race detector. Both native
development binaries built from frozen graphs.

The owner reports that the rebuilt Windows app correctly enters desktop mode,
unlocks and locks storage, starts locked after a real quit/relaunch, and restores
the same identity after another unlock. No real fingerprint belongs in the source
or fixtures. Real OS-provider tests separately passed on Windows and the VMware
Ubuntu Desktop session using disposable credentials. Ubuntu native UI unlock and
quit/reopen identity continuity were pending at this baseline. The subsequent
0.0.2 integration and owner checks pass; see [current status](status.md).
Appearance redesign remains deferred.

The component commands remain red on unpatched LOW GHSA-gcqx-w48m-w2h4
(`deepmerge` through the Svelte Vite plugin). No advisory exception was added.
Full check/package remain blocked by unfinished mutual authentication, live
transport integration and release requirements. A local source commit is useful
as a review/rollback baseline while these limitations remain explicit.

## Inspect the baseline and later changes

From the project root:

```powershell
git status --short
git log -1 --oneline
git show --stat HEAD
git diff
```

No hosting service or remote is required for local source history. Git author
details come from the existing local/global configuration. A source commit does
not publish the app or make its product/security gates pass.

## What comes next

1. Keep subsequent feature changes reviewable; local commits remain controlled
   by the owner's instructions. The 0.0.2 native integration checkpoint is authorized.
2. Retain versioned private candidates and repeat native checks when relevant app
   behavior changes. Ubuntu 0.0.2 startup/storage/restart checks now pass with
   its scoped installation and software rendering; see [native testing](native-testing.md).
3. Review the reciprocal approval and identity-to-transport-key binding protocol.
   Integrate durable activation/revocation before exposing contact history or live
   sessions. Stored contact pins alone grant no session authority.
4. Resolve Tailcat's statistics-label control and packet-boundary direct-only
   enforcement. Keep startup/direct-only gates closed until reviewed controls and
   failure-path evidence exist. Do not deploy a relay as part of this preparation.
5. Connect the tested encrypted queue/acknowledgement path to authenticated sessions
   and test independent Windows/Ubuntu processes, direct and approved relay paths,
   reconnects, restarts, unknown peers and path changes.

Before a public repository or distribution, separately choose the project license,
configure private vulnerability reporting, resolve advisory gates, and complete the
required notices/SBOM, installers, signatures and update-verification work. Nothing
in this local baseline authorizes those publication steps.
