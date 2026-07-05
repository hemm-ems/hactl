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
diagnostic showing which discovery path succeeded or failed and why. Typical
failure reasons:

- `auth_denied` — your long-lived token lacks admin scope. Re-issue from an HA owner account.
- `addon_missing` — the add-on isn't installed. HA → Settings → Add-ons → install `hactl-companion`.
- `protocol_mismatch` — HA Container without Supervisor. Set `COMPANION_URL` in `.env` directly.
- `unreachable` — Supervisor is there but the add-on URL isn't responding. Check Ingress / network.

Discovery requires HA OS or Supervised (`supervisor/api` WS proxy must be
available). External access works automatically via Supervisor-issued
`ingress_session` cookies — no manual port-forwarding or signed-URL setup
needed.
