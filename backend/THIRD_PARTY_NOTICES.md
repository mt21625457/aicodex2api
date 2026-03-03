# Third-Party Notices

## caddyserver/caddy

- Project: https://github.com/caddyserver/caddy
- License: Apache License 2.0
- Copyright:
  Copyright 2015 Matthew Holt and The Caddy Authors

### Usage in this repository

OpenAI WS v2 passthrough relay adopts the Caddy reverse proxy streaming architecture
(bidirectional tunnel + convergence shutdown) and is adapted with minimal changes for
`coder/websocket` frame-based forwarding.

- Referenced commit:
  `f283062d37c50627d53ca682ebae2ce219b35515`
- Referenced upstream files:
  - `modules/caddyhttp/reverseproxy/streaming.go`
  - `modules/caddyhttp/reverseproxy/reverseproxy.go`
- Local adaptation files:
  - `backend/internal/service/openai_ws_v2/caddy_adapter.go`
  - `backend/internal/service/openai_ws_v2/passthrough_relay.go`

The adaptation preserves Apache-2.0 license obligations.

