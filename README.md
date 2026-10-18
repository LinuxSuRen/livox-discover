# livox-discover

Zero-dependency CLI that finds **Livox SDK2 lidars** (Mid360 / Mid360S / Avia / Avia2 ...) on your network in seconds, by listening to their UDP broadcast/push ports and decoding their discovery replies — and can re-IP a lidar without the Livox tools.

Written in Go, standard library only.

## Why

Livox SDK2 has no standalone discovery CLI. Common pain it solves:

- The lidar's factory IP does not match the `lidar_configs[].ip` in your driver JSON — the driver silently receives nothing;
- Multiple Livox units are on the network and you need the real IP of each;
- You want to see which units are actually broadcasting before digging into driver configs;
- You need to move a lidar to another IP and don't want to install the SDK just for that.

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

## Change a lidar's IP

Once a discovery run gave you the lidar's current IP, the `set-ip` subcommand changes the lidar's own IP, subnet mask and gateway through the same SDK2 command channel a driver would use (`kKeyLidarIpCfg`, no registration handshake needed):

```text
$ livox-discover set-ip -lidar 192.168.1.100 -ip 192.168.1.120
[set-ip] 192.168.1.100:56100 -> ip=192.168.1.120 mask=255.255.255.0 gw=192.168.1.1 (from :56101)
[set-ip] attempt 1/3: sent 44 bytes
[ack] from 192.168.1.100  ret_code=0  error_key=0
[set-ip] success: lidar 192.168.1.100 now uses ip 192.168.1.120 mask 255.255.255.0 gw 192.168.1.1
[set-ip] verify with a fresh discovery run; the lidar should now answer from its new IP.
[set-ip] if it stays silent, power-cycle the lidar once and re-run livox-discover.
```

### set-ip flags

| Flag | Default | Meaning |
|---|---|---|
| `-lidar` | — | current IP of the target lidar, as reported by a discovery run (required) |
| `-ip` | — | new lidar IP (required) |
| `-mask` | `255.255.255.0` | new subnet mask |
| `-gw` | derived | new gateway; defaults to the first three octets of `-ip` with a final `.1` |
| `-cmd-port` | `56100` | lidar command port (Mid360/Mid360S/Avia2/Mid360L all use 56100) |

The command is unicast from host port 56101 (falling back to an ephemeral port if a driver holds it) to the lidar's command port, repeated up to 3 times with a 1s timeout each — the same timeout the SDK uses. Exit codes: `0` the lidar accepted, `1` no/rejecting answer, `2` bad usage.

Notes:

- Keep the new IP on the same subnet as this host, otherwise the lidar becomes unreachable after the change.
- After a successful change, run `livox-discover` again to confirm the lidar broadcasts from its new IP; if it stays silent, power-cycle it once.
- The frame carries ip/mask/gateway as one key-value entry; an inconsistent triple is rejected by the lidar (`ret_code != 0`, `error_key = 0x0004`).

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

Key-value control commands (cmd id `0x0100`) carry configuration as `u16 key_num` + `u16 reserved` followed by `key_num` entries of `u16 key` + `u16 length` + value. `set-ip` writes key `0x0004` (`kKeyLidarIpCfg`) with a 12-byte `ip[4] mask[4] gw[4]` value; the lidar answers with a 3-byte ACK payload `{u8 ret_code, u16 error_key}`, and `ret_code == 0 && error_key == 0` means accepted.

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

The frame/CRC parsers and the key-value control payload live in `internal/livox` and are covered by table-driven tests, including byte-exact fixtures generated by a reference implementation of the protocol. The `set-ip` exchange is tested end-to-end against a fake lidar on loopback.

## Contributing

Issues and PRs are welcome. See [LICENSE](LICENSE) (Apache-2.0).
