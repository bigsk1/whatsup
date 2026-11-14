# Tailcat WebUI reference review

Reviewed [ca-x/tailcat-webui](https://github.com/ca-x/tailcat-webui) at immutable
commit [`e7e1a386ca6549c6486cd075b238d68ad930aeb3`](https://github.com/ca-x/tailcat-webui/commit/e7e1a386ca6549c6486cd075b238d68ad930aeb3),
dated 2026-09-04. Its [license file](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/LICENSE#L1-L12)
identifies GNU AGPL version 3. **No source code, styles or assets were imported
into WhatsUp.** This is a source-based design reference, not a security
certification. No external program, installer or test suite was executed.

## Useful concepts

- Keep lifecycle, observed network path and delivery state separate. The project
  defines finite runtime phases and distinguishes direct, DERP and peer-relay
  paths. Its path classification uses a discovery ping; this is an observation,
  not a guarantee about the path of subsequent application packets. WhatsUp must
  also distinguish authenticated acknowledgement from connection readiness.
  Sources: [runtime phases](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/internal/events/envelope.go#L5-L28),
  [path classification](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/internal/tailnet/manager.go#L1088-L1107),
  [operation presentation](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/web/src/components/OperationProgress.tsx#L29-L61).
- Treat events as notifications and reconcile with authoritative backend state.
  The console refreshes state when its SSE stream opens or reconnects, ignores
  stale request completions and rejects older per-operation event sequences.
  Progress notifications can be dropped rather than block engine callbacks.
  WhatsUp can apply this principle through typed Wails events and Go status
  methods, without adding an HTTP/SSE management server.
  Sources: [stream refresh](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/web/src/hooks/useRuntimeEvents.ts#L106-L139),
  [request freshness](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/web/src/hooks/useAsyncResource.ts#L13-L39),
  [sequence checks](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/web/src/pages/ClientsPage.tsx#L175-L190),
  [bounded event broker](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/internal/events/broker.go#L14-L24).
- Close admission before changing authority, then cancel and drain active work.
  Its runtime tracks connections behind an admission mutex and stopping flag.
  The manager stops live servers before first enabling an allowlist or revoking
  a client, and explicitly handles Tailcat's empty-list-means-allow-all behavior.
  These are useful failure cases for WhatsUp's exact-peer admission tests; the
  WebUI's multi-client policy does not satisfy WhatsUp's two-identity invariant.
  Sources: [runtime shutdown](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/internal/tailnet/runtime_tailcat.go#L158-L230),
  [authority changes](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/internal/tailnet/manager.go#L871-L954),
  [enabled-empty allowlist](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/internal/tailnet/manager.go#L1201-L1212).
- Revisit transfer lifecycle and progress when attachments are implemented.
  Provisional and active streams carry a generation so revoked work cannot
  become authorized later. Progress includes bytes, file counts and explicit
  canceled, interrupted and expired outcomes. WhatsUp still needs its own
  receiver approval and authenticated-contact integration.
  Sources: [transfer admission](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/internal/transfer/share_gate.go#L103-L125),
  [revocation and drain](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/internal/transfer/share_gate.go#L154-L215),
  [transfer progress](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/web/src/components/TransferProgress.tsx#L16-L48).

## Architecture and evidence limits

The project is a multi-user, OIDC-authenticated web control plane with multiple
Tailcat instances, arbitrary TCP tunnels, port forwarding, exit-node controls and
public resource publishing. Its React/Ant Design administration console and
environment-supplied encryption master key serve that architecture. WhatsUp uses
a local Wails/Svelte messenger, an OS credential-backed vault and no generic
network-management API or public content server. Sources:
[project scope](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/README.md#L15-L86),
[secret box](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/internal/secrets/box.go#L17-L60).

Its [module graph](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/go.mod#L1-L22)
pins Tailcat v0.5.0, older than WhatsUp's reviewed immutable revision. The
inspected adapter does not provide WhatsUp's reciprocal identity approval,
per-contact credential lifecycle, packet-boundary direct-only enforcement or
relay application-statistics label control. Omitted adapter fields do not mean
upstream behavior is disabled. Its formatted upstream debug logging also differs
from WhatsUp's fixed-code redacted diagnostics. Sources:
[adapter construction](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/internal/tailnet/runtime_tailcat.go#L38-L96),
[runtime specifications](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/internal/tailnet/runtime.go#L40-L58),
[upstream logging](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/internal/tailnet/manager.go#L1319-L1321).

The repository includes a real local DERP/STUN multi-server startup test, but it
was not run during this review and does not demonstrate WhatsUp's two-device
pairing or messaging security. [Test scope](https://github.com/ca-x/tailcat-webui/blob/e7e1a386ca6549c6486cd075b238d68ad930aeb3/internal/tailnet/multi_integration_test.go#L11-L30).
No stack migration or dependency change follows from this reference review.
