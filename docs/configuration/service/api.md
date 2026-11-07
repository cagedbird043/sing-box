---
icon: material/new-box
---

!!! question "Since sing-box 1.14.0"

# sing-box API

The sing-box API service is a gRPC server for observing and controlling the running sing-box instance.

It can be accessed by the [sing-box graphical clients](/clients/) for iOS, macOS, and
Android (via the Remote Control feature), or the
[sing-box dashboard](https://github.com/SagerNet/sing-box-dashboard).

The server also accepts [gRPC-Web](https://github.com/grpc/grpc/blob/master/doc/PROTOCOL-WEB.md) requests.

### ProviderService

The native `daemon.ProviderService` uses the same API listener, transports, and Bearer
authentication as the other sing-box API methods. Configure a non-empty `secret` to
protect subscription information.

`ListProviders`, `GetProvider`, and `SubscribeProviders` return a `Provider.url`
field (protobuf field 7). For remote providers this is the exact configured
subscription URL; for local and inline providers it is empty. Provider responses
do not expose request headers, local file paths, or other provider configuration.
Existing subscription traffic, expiry, node, refresh, and health-check fields are
unchanged.

Subscription URLs may contain credentials. Clients should hide them by default
and reveal or copy them only on explicit user action; do not write them to logs.

### Probe groups

A `probe` group names a `node-probe` service (`probe`) and one of its `policy` entries. The service measures members through each of them (`measures`: `download` with a ranged body of `bytes`, or `request` where any HTTP response counts), keeps the latest `window` samples per member and measure, and counts the routed connections each probe group carried through each member over `traffic_window`. The policy's expressions ([expr](https://expr-lang.org/) language, compiled and type-checked at load) decide `eligible`, `qualify`, `score` (lower is better), `switch` and `penalty` (seconds). Unknown measures, unknown variables and wrong result types fail `sing-box check`.

A screening (startup, `screen.interval`, network changes with `screen.on_network_change`, or `URLTest`) measures every eligible member, evaluates all of them, and selects the best qualified, unpenalized member when the group has no current member or the current one is ineligible, or when `switch` holds. A check (`check.interval`, or a failed connection through the current member with `check.on_traffic_failure`, at most once per `check.cooldown`) measures only the current member; when it no longer qualifies, other members are measured in score order and the first that qualifies is selected. Nothing is selected before it qualifies. With `domestic` configured, a failed direct fetch skips judging and switching.

Expression variables (durations in seconds, throughput in bytes per second): `m.<measure>` with `count`, `ok_rate`, `fail_streak`, `age`, `last_ok`, `last_error`, `first_byte`/`transfer`/`total`/`throughput` each as `last`/`p50`/`p90`/`min`/`max`/`mean`, and `targets["<url>"]` with the same fields per target; `traffic` with `conns`, `ok`, `dial_failed`, `aborted`, `fail_rate`, `bytes_down`, `bytes_up`, `last_failure_age`; `node` (`tag`, `index`, `type`); `is_current`, `selected_for`, `penalized`, `penalty_left`; `domestic` (`ok`, `configured`); `now` (`hour`, `minute`, `weekday`, `unix`, local time); `group` (`tag`, `size`, `current`); and the constants `ms`, `s`, `minute`, `hour`, `KiB`, `MiB`. `penalty` also sees `qualified` and `score`. `switch` sees `current` and `best`, each with `tag`, `index`, `qualified`, `score`, `penalized`, `m` and `traffic`, plus `domestic`, `now`, `group` and the constants. Defaults: eligible `true`, qualify `true`, score `node.index`, switch `!current.qualified || best.score < current.score`, penalty `0`.

`StartedService.SubscribeGroups` includes `Group.probe` for `probe` groups: `policy`, the effective `expressions` (eligible, qualify, score, switch, penalty), `screenMeasures`, `checkMeasures`, `selectedAt`, `switchReason`, `trafficWindowMs`, `running`, `generation`, `screenedAt`, and `error`.

Calling `StartedService.URLTest` for a probe group requests a screening of its policy. Queued or running requests coalesce. Start and completion publish updates even without selection changes. Every attempt increments `generation`, clears `running`, and updates `screenedAt` (Unix milliseconds), including offline, paused, cancelled, or skipped attempts. Capture the previous `generation`, wait for `running == false` with a greater `generation`, and display nonempty `error`.

`GroupItem.probe` is absent for a member that has neither an evaluation, a sample, nor a counted connection. Otherwise it contains the latest verdict (`time`, `eligible`, `ok` = qualified and not penalized, `error` naming the failing qualify clause or evaluation error, `hasScore`, `score`, `penaltyUntil`), per-measure statistics (`measures`: `count`, `okRate`, `lastTime`, `lastOk`, `lastError`, and `last/p50/p90/min/max` of `firstByteMs`, `transferMs`, `totalMs` and `throughput` in bytes per second over successful samples), and `traffic` (`conns`, `ok`, `dialFailed`, `aborted`, `failRate`, `bytesDown`, `bytesUp`) within the traffic window.

### Structure

```json
{
  "type": "api",
  
  ... // Listen Fields
  
  "secret": "",
  "access_control_allow_origin": [],
  "access_control_allow_private_network": false,
  "dashboard": {
    "enabled": true,
    "path": "",
    "download_url": "",
    "http_client": "", // or {}
    "update_interval": ""
  },
  "tls": {}
}
```

### Listen Fields

See [Listen Fields](/configuration/shared/listen/) for details.

### Fields

#### secret

Secret for the API.

Clients authenticate with the standard `authorization: Bearer <secret>` gRPC metadata header.

If empty, authentication is disabled.

#### access_control_allow_origin

CORS allowed origins, `*` will be used if empty.

#### access_control_allow_private_network

Allow access from private network.

#### dashboard

Web dashboard downloaded and served over the API listener at `/dashboard/`; other browser
requests are redirected to it.

!!! info ""

    The object can be replaced with a boolean value (equivalent to `{ "enabled": <bool> }`),
    or with a string path (equivalent to `{ "enabled": true, "path": "<string>" }`).

##### enabled

Enable the dashboard.

##### path

Directory the dashboard files are stored in.

`dashboard` in the working directory will be used by default.

If the directory contains files not downloaded by sing-box, they are served as-is.

##### download_url

Download URL of the dashboard archive (zip).

`https://github.com/SagerNet/sing-box-dashboard/archive/refs/heads/gh-pages.zip` will be used by default.

##### http_client

HTTP client used to download the dashboard.

See [HTTP Client Fields](/configuration/shared/http-client/) for details.

##### update_interval

Update interval of the dashboard.

`1d` will be used by default.

#### tls

TLS configuration, see [TLS](/configuration/shared/tls/#inbound).
