# hactl Setup (for humans)

Agent-facing usage lives in [`manual.md`](manual.md) (`hactl rtfm`); this page
covers the one-time setup and connectivity troubleshooting.

## Instance configuration

`hactl setup` creates a `.env` in the current directory:

```
HA_URL=http://homeassistant.local:8123
HA_TOKEN=<long_lived_access_token>
```

> **Windows users:** Use `HA_URL=http://127.0.0.1:8123` instead of `localhost`.
> Windows may resolve `localhost` to `::1` (IPv6), but HA typically listens on
> `0.0.0.0` (IPv4 only), causing connection failures.

Point hactl at the directory containing `.env`:

```bash
export HACTL_DIR=/path/to/instance   # or
hactl --dir /path/to/instance <cmd>  # or cd into it
```

Without `--dir`/`HACTL_DIR`, hactl uses the `.env` in the current directory,
then walks parent directories (git-style; parent `.env` files without `HA_URL`
are skipped), then falls back to `~/.hactl/default/`.

## Debugging connectivity

Set `HACTL_LOG_LEVEL=debug` to surface discovery, WS, and HTTP details on
stderr (accepts `debug`, `info`, `warn`, `error`; defaults to `info`).

Companion connectivity issues? Run `hactl companion status` for a one-screen
diagnostic showing which discovery path succeeded or failed and why. It **exits
1 when the companion is not usable**, so it can gate a script. Failure reasons:

- `auth_denied` — your long-lived token lacks admin scope. Re-issue from an HA owner account.
- `auth_invalid` — Home Assistant rejected the token outright. The connection is fine; replace `HA_TOKEN` in `.env`.
- `addon_missing` — the add-on isn't installed. HA → Settings → Add-ons → install `hactl-companion`.
- `protocol_mismatch` — HA Container without Supervisor. Set `COMPANION_URL` in `.env` directly.
- `redirected` — `HA_URL` names an origin that redirects elsewhere. See below.
- `unreachable` — nothing answered at `HA_URL`. Check the URL and the network.

### `HA_URL` must be the origin that answers

hactl talks to the URL you configured; a redirect to a different scheme, host or
port is refused, naming the origin to put in `.env`.

The case this comes up in is `HA_URL=http://…` behind a reverse proxy that 301s
to `https://`. It is worth refusing rather than following, because following it
half-works: REST calls follow the redirect transparently with the credentials
intact, so `ent ls` returns your real entities, while WebSocket-backed
subsystems — traces, registries, add-on discovery, recorder error counts —
cannot follow a redirect at all, there being no such step in the protocol. That
produced a `health` output with a real version, state, location and timezone
beside `errors: -1` and `companion: not found`, at exit 0, with nothing naming
the scheme as the cause.

Same-origin redirects (a trailing slash, a path rewrite) are followed normally.

Discovery requires HA OS or Supervised (`supervisor/api` WS proxy must be
available). External access works automatically via Supervisor-issued
`ingress_session` cookies — no manual port-forwarding or signed-URL setup
needed.
