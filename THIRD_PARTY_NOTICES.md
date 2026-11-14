# Third-party source and assets

This file records third-party material retained in this source tree. It does not
select a license for WhatsUp's original code or grant rights to third-party marks.
No public license for WhatsUp has been selected.

## Wails

The retained build assets originated from the Wails v3.1.0 scaffolding: the icons
at `build/appicon.png` and `build/windows/icon.ico`, platform build and installer
templates under `build/`, and generated TypeScript bindings under
`frontend/bindings/`. The local recipes and metadata have been adapted for WhatsUp.
These files are derived from Wails source distributed under the MIT License.

Copyright (c) 2018-Present Lea Anthony.

The full notice is retained at [third_party/licenses/wails-MIT.txt](third_party/licenses/wails-MIT.txt).
Source: [Wails v3.1.0](https://github.com/wailsapp/wails/tree/v3.1.0),
[upstream license](https://github.com/wailsapp/wails/blob/v3.1.0/LICENSE).
The current icons are scaffold placeholders, not WhatsUp branding.

The application applies a private, reviewed native integration patch to Wails
3.1.0 for Go-only file drops and bounded completed-reader media responses. The
patch and immutable upstream basis are recorded in `config/wails-patch.json`.
The exact upstream module and its MIT notice are preserved during the build.

## Tink

Attachment storage uses `github.com/tink-crypto/tink-go/v2` version 2.9.0,
distributed under the Apache License, Version 2.0. Its full license is retained
at [third_party/licenses/tink-Apache-2.0.txt](third_party/licenses/tink-Apache-2.0.txt).
Source: [Tink Go 2.9.0](https://github.com/tink-crypto/tink-go/tree/v2.9.0).

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

## Dependency and binary distribution boundary

Go/npm dependencies are resolved through their manifests, lockfiles and checksum
records. Apart from the reviewed Tailcat library copy above, their full source is
not vendored here. Other dependencies retain their own licenses. This source
notice is not a complete binary license inventory or SBOM. Before distributing binaries, inventory direct
and transitive dependencies, retain all required copyright/license notices, and
complete the distribution requirements described in [security information](SECURITY.md).
The current preview is unsigned and is not an approved public release.
