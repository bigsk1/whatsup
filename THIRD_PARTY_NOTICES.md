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

## Dependency and binary distribution boundary

Go/npm dependencies are resolved through their manifests, lockfiles and checksum
records; their full source is not vendored here. Tailcat is BSD-3-Clause, and the
other dependencies retain their own licenses. This source notice is not a complete
binary license inventory or SBOM. Before distributing binaries, inventory direct
and transitive dependencies, retain all required copyright/license notices, and
complete the release gates documented in `docs/development.md`. The current
development build and this initial Git baseline do not authorize publication.
