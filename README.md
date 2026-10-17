# livox-discover

Zero-dependency CLI that finds **Livox SDK2 lidars** (Mid360 / Mid360S / Avia / Avia2 ...) on your network in seconds, by listening to their UDP broadcast/push ports and decoding their discovery replies.

Written in Go, standard library only.

## Why

Livox SDK2 has no standalone discovery CLI. Common pain it solves:

- The lidar's factory IP does not match the `lidar_configs[].ip` in your driver JSON — the driver silently receives nothing;
- Multiple Livox units are on the network and you need the real IP of each;
- You want to see which units are actually broadcasting before digging into driver configs.

## Install

Prebuilt binaries for Linux and macOS (amd64 / arm64) are attached to [GitHub Releases](https://github.com/LinuxSuRen/livox-discover/releases):

```bash
curl -fsSL https://github.com/LinuxSuRen/livox-discover/releases/download/v0.1.0/livox-discover-v0.1.0-linux-amd64.tar.gz | tar xz
./livox-discover-v0.1.0-linux-amd64/livox-discover -duration 15s
```

With a Go toolchain (1.22+):

```bash
go install github.com/LinuxSuRen/livox-discover@latest
```

From a checkout:

```bash
go build .
```

## Quick start

Stop any driver that occupies the ports first (e.g. `pkill -f livox_lidar_nod[e]`), then run the tool:

```text
$ livox-discover
[listen] 0.0.0.0:55000
[listen] 0.0.0.0:56000
[listen] 0.0.0.0:56201
listening 15s ...
[hit] src=192.0.2.10:56101 -> local:56000  len=48  head=aa 00 30 00 2a 00 00 00 00 00 01 01 00 00 00 00  cnt=1
[discovery] src=192.0.2.10  dev=Mid360(9)  sn=TFD2DJAAB00001  lidar_ip=192.0.2.10  cmd_port=56100
=== summary ===
src 192.0.2.10     packets=15    livox_header=15    ports=56000:15
    dev=Mid360(9)  sn=TFD2DJAAB00001  lidar_ip=192.0.2.10  cmd_port=56100  ret_code=0
```

The `src` IP in the summary is the lidar's address — paste it into the `ip` field of your driver config. Whenever a datagram decodes as a SDK2 discovery ACK, the decoded device type, serial number, lidar IP and command port are shown too.

### Flags

| Flag | Default | Meaning |
|---|---|---|
| `-duration` | `15s` | how long to listen (Go duration syntax: `15s`, `500ms`, `2m`) |
| `-ports` | `55000,56000,56201` | comma-separated UDP ports to listen on |
| `-json` | off | print one machine-readable JSON report instead of the text log |

`Ctrl-C` stops listening early and still prints the report (exit code 130). Exit codes: `0` normal, `1` when no port could be bound at all, `2` for invalid flags.

### JSON output

`-json` keeps stdout to the report alone (progress goes to stderr), so it can be piped straight into other tools:

```bash
livox-discover -json -duration 10s | jq -r '.sources[].discoveries[] | "\(.sn) \(.lidar_ip) \(.dev_type_name)"'
```

```json
{
  "duration_seconds": 10,
  "ports": [55000, 56000, 56201],
  "sources": [
    {
      "ip": "192.0.2.10",
      "packets": 15,
      "livox_packets": 15,
      "ports": { "56000": 15 },
      "discoveries": [
        {
          "ret_code": 0,
          "dev_type": 9,
          "dev_type_name": "Mid360",
          "sn": "TFD2DJAAB00001",
          "lidar_ip": "192.0.2.10",
          "cmd_port": 56100
        }
      ]
    }
  ]
}
```

## How it works

Livox SDK2 devices periodically answer discovery queries by broadcasting UDP packets that use the SDK2 command frame format. The tool passively binds the SDK2 broadcast/push ports with `SO_REUSEADDR`, counts every sender it observes, and fully validates + decodes every discovery ACK.

Command frame header (24 bytes, little-endian):

| Offset | Size | Field |
|---|---|---|
| 0 | 1 | start of frame, always `0xAA` |
| 1 | 1 | protocol version |
| 2 | 2 | total frame length, header included |
| 4 | 4 | sequence number |
| 8 | 2 | command id (`0x0000` = discovery) |
| 10 | 1 | command type (`0x01` = ACK) |
| 11 | 1 | sender type (`0x01` = lidar) |
| 12 | 6 | reserved |
| 18 | 2 | CRC-16/CCITT-FALSE over bytes 0–17 |
| 20 | 4 | CRC-32 (IEEE) over the payload |
| 24 | … | payload |

Both CRCs are verified before a frame is trusted; a packet whose first byte is `0xAA` but fails validation is still counted (as a `livox_header` packet), just not decoded.

Discovery ACK payload (24 bytes): `ret_code` (u8), `dev_type` (u8), `sn` (char[16], NUL padded), `lidar_ip` (u8[4] dotted quad), `cmd_port` (u16, little-endian). Known dev types: `9` = Mid360, `35` = Mid360S, `40` = Avia2, `41` = Mid360L.

Ports: `56000` is the SDK2 discovery broadcast port, `56201` carries status pushes, and `55000` is an SDK1 legacy port kept for compatibility. All facts above come from the public SDK2 protocol.

## Usage notes

- A source that shows up on port 56000 with `0xAA`-headed packets is an SDK2 lidar; sources that never appear are either powered off, on another subnet (data-plane is unicast and must be same-subnet), or filtered.
- Run it on the host that the lidar sends data to (same NIC/subnet as configured in `host_net_info`).
- `-ports` accepts any comma-separated list, e.g. `-ports 56000` for a focused scan or a high port for testing.

## Development

```bash
gofmt -l .
go vet ./...
go test ./...
```

The frame/CRC parsers live in `internal/livox` and are covered by table-driven tests, including byte-exact fixtures generated by a reference implementation of the protocol.

## Contributing

Issues and PRs are welcome. See [LICENSE](LICENSE) (Apache-2.0).
