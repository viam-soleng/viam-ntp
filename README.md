# Module viam-ntp

Queries `pool.ntp.org` once an hour and steps the host's system clock to match. Useful on machines without a working `ntpd`/`chronyd` or a battery-backed RTC, where a wrong clock breaks TLS, data timestamps, and log correlation.

## Models

This module provides the following model(s):

- [`viam:viam-ntp:update`](viam_viam-ntp_update.md) - generic service that reads the time over SNTP and sets the system clock via `DoCommand`

## Requirements

Setting the clock requires elevated privileges: `viam-server` must run as root on Linux (or with `CAP_SYS_TIME`) and macOS, or as Administrator on Windows. Without them, `query` and `status` still work and `update` fails with a permission error.

Clock writes are supported on Linux, macOS, and Windows. Windows resolves only to the millisecond, so corrections there are coarser than on Unix.

If `systemd-timesyncd`, `chronyd`, `ntpd`, or the Windows Time service is already running, disable it first — two things stepping the same clock will fight. On Windows: `w32tm /unregister`.

## Building

`make module.tar.gz` builds for the host and is what the Viam cloud build runs. On a Windows target it rewrites the `entrypoint` in `meta.json` to `bin/viam-ntp.exe` before packaging, since viam-server resolves that path literally, then restores the working copy.

To cross-compile everything locally:

```sh
make packages                  # one tarball per arch under bin/<goos>-<goarch>/
make upload VERSION=1.2.3      # prints the four `viam module upload` commands
```

`make upload` prints rather than runs the uploads, so you can check the version and platforms before publishing. Requires `jq`.
