# Third-party source and assets

This file records third-party material retained in this source tree. It does not
select a license for WhatsUp's original code or grant rights to third-party marks.
WhatsUp's original code is covered by the [MIT license](LICENSE), copyright
bigsk1. The third-party terms and notices below remain separate.

## Wails

The retained build assets originated from the Wails v3.1.0 scaffolding: platform
build and installer templates under `build/` and generated TypeScript bindings
under `frontend/bindings/`. The local recipes and metadata have been adapted for
WhatsUp. These files are derived from Wails source distributed under the MIT License.

Copyright (c) 2018-Present Lea Anthony.

The full notice is retained at [third_party/licenses/wails-MIT.txt](third_party/licenses/wails-MIT.txt).
Source: [Wails v3.1.0](https://github.com/wailsapp/wails/tree/v3.1.0),
[upstream license](https://github.com/wailsapp/wails/blob/v3.1.0/LICENSE).
The application icons (`build/appicon.svg`, `build/appicon-small.svg`,
`build/appicon.png` and `build/windows/icon.ico`) and the in-app logo are original
WhatsUp artwork and no longer derive from the Wails placeholder icon.

The application applies a private, reviewed native integration patch to Wails
3.1.0 for Go-only file drops and bounded drag-state hints, bounded
completed-reader media responses, minimal Windows notification activation metadata
and errors when native clipboard writes fail. The
patch and immutable upstream basis are recorded in `config/wails-patch.json`.
The exact upstream module and its MIT notice are preserved during the build.

## Tink

Attachment storage and encrypted recovery archives use `github.com/tink-crypto/tink-go/v2` version 2.9.0,
distributed under the Apache License, Version 2.0. Its full license is retained
at [third_party/licenses/tink-Apache-2.0.txt](third_party/licenses/tink-Apache-2.0.txt).
Source: [Tink Go 2.9.0](https://github.com/tink-crypto/tink-go/tree/v2.9.0).

## Pion RTP and SRTP

The real-time media protection code under `internal/callmedia` uses
`github.com/pion/rtp` version 1.10.6 and `github.com/pion/srtp/v3` version 3.1.3.
They bring in `github.com/pion/transport/v5` 5.1.1, `github.com/pion/rtcp`
1.2.19, `github.com/pion/logging` 0.2.4 and `github.com/pion/randutil` 0.1.0.
All six are resolved through `go.mod` and `go.sum`; none is vendored or modified.

These modules are distributed under the MIT License. Their copyright notices
and the license text are retained at
[third_party/licenses/pion-MIT.txt](third_party/licenses/pion-MIT.txt). The
`utils/xor` package of `pion/transport` also carries a BSD 3-Clause notice from
The Go Authors, retained at
[third_party/licenses/pion-transport-xor-BSD-3-Clause.txt](third_party/licenses/pion-transport-xor-BSD-3-Clause.txt).

Source: [pion/rtp 1.10.6](https://github.com/pion/rtp/tree/v1.10.6),
[pion/srtp 3.1.3](https://github.com/pion/srtp/tree/v3.1.3).

## Tailcat

A reviewed library-only source copy is retained under `third_party/tailcat` and
selected by the root Go module's local replacement. Its upstream basis is
`github.com/tailscale/tailcat` revision
`e00cdde6ff787f801361ff85ed085775c775d4c5`, recorded as
`v0.7.1-0.20261105151022-e00cdde6ff78`.

Copyright (c) 2020 Tailscale Inc & contributors.

Tailcat is distributed under the BSD 3-Clause License. The full copyright,
redistribution conditions and disclaimer are retained in
[third_party/tailcat/LICENSE](third_party/tailcat/LICENSE), including the notice
required for future binary distributions. Its names and contributors' names do
not endorse WhatsUp.

WhatsUp's local changes add an explicit opt-out for optional DERP application
statistics and disable bearer-address debug logging. Unneeded upstream CLI,
web demo, release assets and upstream tests are omitted; retained library helpers
are not exposed as WhatsUp services. The reviewed source basis, changed files,
local test and license hashes are recorded in
[config/tailcat-patch.json](config/tailcat-patch.json). The workflow rejects
unexpected replacements or file changes. This limited patch does not add
direct-only enforcement.

Source: [reviewed Tailcat revision](https://github.com/tailscale/tailcat/tree/e00cdde6ff787f801361ff85ed085775c775d4c5),
[upstream BSD-3-Clause license](https://github.com/tailscale/tailcat/blob/e00cdde6ff787f801361ff85ed085775c775d4c5/LICENSE).

## Interface fonts

The interface bundles these fonts from `@fontsource` packages version 5.3.0. Each
is distributed under the SIL Open Font License, Version 1.1, and is served only
from the application bundle, never from a remote font service.

| Font | Copyright | License |
| --- | --- | --- |
| Nunito | 2014 The Nunito Project Authors | [OFL-1.1](third_party/licenses/fonts/nunito-OFL-1.1.txt) |
| Press Start 2P | 2012 The Press Start 2P Project Authors, Reserved Font Name "Press Start 2P" | [OFL-1.1](third_party/licenses/fonts/press-start-2p-OFL-1.1.txt) |
| VT323 | 2011 The VT323 Project Authors | [OFL-1.1](third_party/licenses/fonts/vt323-OFL-1.1.txt) |
| Orbitron | 2018 The Orbitron Project Authors | [OFL-1.1](third_party/licenses/fonts/orbitron-OFL-1.1.txt) |
| Rajdhani | 2014 Indian Type Foundry | [OFL-1.1](third_party/licenses/fonts/rajdhani-OFL-1.1.txt) |
| JetBrains Mono | 2020 The JetBrains Mono Project Authors | [OFL-1.1](third_party/licenses/fonts/jetbrains-mono-OFL-1.1.txt) |

## Interface icons

The Bubbly and Neon City looks use icons from `phosphor-svelte` version 3.1.1,
Copyright (c) 2020 Phosphor Icons, distributed under the MIT License. The full
notice is retained at [third_party/licenses/phosphor-MIT.txt](third_party/licenses/phosphor-MIT.txt).

The Arcade and Terminal looks use path data adapted from pixelarticons version
2.4.2, Copyright (c) 2019 Gerrit Halfmann, distributed under the MIT License, in
`frontend/src/lib/icons/pixel-paths.ts`. The full notice is retained at
[third_party/licenses/pixelarticons-MIT.txt](third_party/licenses/pixelarticons-MIT.txt).

## Debian build-tool archive writer

The isolated packaging tool retains a writer-only subset of
`github.com/blakesmith/ar` at `v0.0.0-20190502131153-809d4375e1fb`. It contains
the upstream header and writer with formatting changes, its unchanged
[MIT notice](third_party/ar-writer/COPYING), and a local module declaration.
The archive reader is not retained. This subset is used only to produce Debian
packages, not by the WhatsUp application. Exact retained bytes and their source
are recorded in [config/nfpm-ar-writer.json](config/nfpm-ar-writer.json).

## Dependency and binary distribution boundary

Go/npm dependencies are resolved through their manifests, lockfiles and checksum
records. Apart from the reviewed Tailcat and build-tool subsets above, their full source is
not vendored here. Other dependencies retain their own licenses. This source
notice is not a complete binary license inventory or SBOM. Before distributing binaries, inventory direct
and transitive dependencies, retain all required copyright/license notices, and
complete the distribution requirements described in [security information](SECURITY.md).
The current preview is unsigned and is not an approved public release.
