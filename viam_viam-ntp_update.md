# Model viam:viam-ntp:update

A generic service that queries `pool.ntp.org` over SNTP and steps the host's system clock, hourly and on demand. The server, the one-hour interval, the 5s timeout, and the 100ms threshold below which a correction is not worth applying are all fixed.

The background loop always runs; `sync_on_start` only controls whether the first check happens immediately instead of an hour in.

Changing the clock needs privileges: root on Linux (or `CAP_SYS_TIME`) and macOS, Administrator on Windows. Without them `query` and `status` still work and `update` fails with a clear error. Check `can_set_clock` in `status` to see where you stand.

Clock writes work on every platform the module builds for:

| Platform          | Implementation                                               |
|-------------------|--------------------------------------------------------------|
| Linux (+ Android) | `clock_settime(CLOCK_REALTIME)`, nanosecond resolution       |
| macOS (+ iOS)     | `settimeofday`; turn off `systemsetup -setusingnetworktime`  |
| Windows           | `SetSystemTime`, millisecond resolution; needs Administrator |

## Configuration

```json
{
  "sync_on_start": true
}
```

### Attributes

| Name            | Type | Inclusion | Default | Description                                                      |
|-----------------|------|-----------|---------|------------------------------------------------------------------|
| `sync_on_start` | bool | Optional  | `false` | Run one update immediately at startup, ahead of the hourly loop. |

### Example Configuration

Keep a Raspberry Pi with no RTC on time from boot:

```json
{
  "sync_on_start": true
}
```

## DoCommand

Commands take either form:

```json
{"update": {"force": true}}
{"command": "update", "force": true}
```

### `query`

Reports what the server says. Never touches the clock.

```json
{"query": {}}
```

Returns `server`, `ntp_time`, `system_time`, `offset` / `offset_ms` (positive means the local clock is behind), `round_trip_ms`, and `stratum`.

### `update` (alias `sync`)

Queries NTP and steps the clock.

```json
{"update": {}}
{"update": {"force": true}}
```

| Argument  | Type | Description                                     |
|-----------|------|-------------------------------------------------|
| `force`   | bool | Apply the correction even if it is under 100ms. |
| `dry_run` | bool | Measure and report without writing the clock.   |

Returns the `query` fields plus `applied` (bool) and, when the write was skipped, a `reason`.

### `set_time`

Sets the clock directly, for machines with no route to an NTP server.

```json
{"set_time": {"time": "2026-08-17T17:28:22Z"}}
{"set_time": {"unix": 1786001302.5}}
```

Supply either `time` (RFC3339) or `unix` (epoch seconds). `dry_run` is honored.

### `status`

```json
{"status": {}}
```

Returns the current `system_time` / `local_time` / `timezone`, the `server`, `sync_interval`, `can_set_clock` (does this process hold the privilege to write the clock), and `last_sync` — the result of the most recent attempt, whether from the loop or a command, including any error. `last_sync` is `null` until the first attempt.

## Command line

The module also builds a small CLI for checking a machine without configuring it into a robot:

```sh
go run ./cmd/cli        # query only
sudo go run ./cmd/cli -set   # query and set
```
