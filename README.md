# livox-discover

Zero-dependency CLI that finds **Livox SDK2 lidars** (Mid360 / Mid360S / Avia / Avia2 ...) on your network in seconds, by listening to their UDP broadcast/push ports and decoding their discovery replies — and can re-IP a lidar without the Livox tools, from the CLI or a small built-in web UI. It also finds **Hesai lidars** (JT128 / AT128 family) through the point clouds they stream, and re-IPs / reboots them over their PTC control channel.

Written in Go, standard library only.

## Why

Livox SDK2 has no standalone discovery CLI. Common pain it solves:

- The lidar's factory IP does not match the `lidar_configs[].ip` in your driver JSON — the driver silently receives nothing;
- Multiple Livox units are on the network and you need the real IP of each;
- You want to see which units are actually broadcasting before digging into driver configs;
- You need to move a lidar to another IP and don't want to install the SDK just for that.

Hesai lidars have no discovery protocol at all, but they stream MSOP point-cloud packets to UDP 2368 — factory-default units broadcast them — so the same listen-and-report approach finds them (`-vendor hesai`), and the same set-ip / reboot workflow moves them to another subnet over PTC (TCP 9347).

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
| `-probe` | off | also broadcast a discovery query every second from every IPv4 interface; some lidars stay silent unless queried |
| `-vendor` | `livox` | comma-separated lidar families to listen for: `livox`, `hesai` (adds the 2368 MSOP port) |
| `-json` | off | print one machine-readable JSON report instead of the text log |

`Ctrl-C` stops listening early and still prints the report (exit code 130). Exit codes: `0` normal, `1` when no port could be bound at all, `2` for invalid flags.

### JSON output

`-json` keeps stdout to the report alone (progress goes to stderr), so it can be piped straight into other tools. Each source additionally carries `hesai_packets` when it was recognized as a Hesai MSOP stream:

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

- **A saved IP only takes effect after the lidar restarts** (verified on a Mid360S): the ACK means "stored", the runtime IP stays on the old value until then. Use the `reboot` subcommand — or power-cycle — then verify with `livox-discover -probe`.
- Keep the new IP on the same subnet as this host, otherwise the lidar becomes unreachable after the change.
- To change the IP of a lidar that currently sits in another subnet than your NIC, first add a temporary address or host route on the subnet the lidar is in (`sudo ifconfig <iface> alias <lidar-net>.50 255.255.255.0` on macOS, `sudo ip addr add <lidar-net>.50/24 dev <iface>` on Linux); the tool prints the matching hint when it detects the situation.
- The frame carries ip/mask/gateway as one key-value entry; an inconsistent triple is rejected by the lidar (`ret_code != 0`, `error_key = 0x0004`).

## Hesai lidars (JT128 / AT128 family)

Hesai lidars answer no discovery queries, but they stream their MSOP point clouds to UDP 2368 — a factory-default unit broadcasts 1100-byte packets from source port 10000, so passively listening finds it and its real IP:

```text
$ livox-discover -vendor hesai -duration 5s
[listen] 0.0.0.0:56000
[listen] 0.0.0.0:2368
listening 5s ...
[hit] src=192.168.124.20:10000 -> local:2368  len=1100  head=ee ff 01 04 89 00 80 02 ...  cnt=1
[hesai] src=192.168.124.20:10000 -> local:2368  len=1100  Hesai MSOP point cloud — the src IP is the lidar's address
=== summary ===
src 192.168.124.20  packets=44839  livox_header=0      ports=2368:44839
    hesai_msop=44839  Hesai MSOP point cloud; the src IP is the lidar's address
```

(Real capture from a JT128 on a robot sensor network; `-vendor livox,hesai` listens for both families at once.)

### Change a Hesai lidar's IP

`set-ip -vendor hesai` talks PTC, the TCP control channel the Hesai SDK itself uses, so no vendor tools or SDK build is needed:

```text
$ livox-discover set-ip -vendor hesai -lidar 192.168.1.201 -ip 192.168.124.20 -gw 192.168.124.1
[set-ip] 192.168.1.201:9347 (PTC) -> ip=192.168.124.20 mask=255.255.255.0 gw=192.168.124.1
[ptc] channel ok (calibration answer: 4096 bytes)
[set-ip] success: lidar 192.168.1.201 stores ip 192.168.124.20 mask 255.255.255.0 gw 192.168.124.1
[set-ip] a stored change only takes effect after the lidar restarts, and the runtime IP stays on the old value until then.
[set-ip] apply it now: livox-discover reboot -vendor hesai -lidar 192.168.1.201  (the reboot still targets the current IP)
```

Flags: `-lidar` (current IP, required), `-ip` / `-mask` / `-gw` (same semantics as the Livox path), `-ptc-port` (default 9347). The read-only calibration query doubles as a channel probe before anything is written.

`reboot -vendor hesai` restarts the lidar over the same channel — verified on a JT128: a reboot takes about half a minute, after which the point clouds come from the stored address.

Notes:

- **A stored SetNet only applies across a restart** (verified on a JT128, and in the Hesai SDK docs): the ACK means "stored", the runtime IP stays on the old value until the lidar reboots. That is why the reboot above still targets the *current* IP.
- Factory default for the JT128/AT128 family is `192.168.1.201/24`. When the host sits on another subnet, ping and PTC both fail while the MSOP broadcast keeps flowing — exactly the "silent driver" trap. The summary then prints the same `[hint]` lines as for Livox: add a temporary address (`sudo ip addr add 192.168.1.20/24 dev <iface>`) or host route, run `set-ip`, then reboot.
- A Velodyne on the same port is not misreported: its data packets start `ff ee` where Hesai starts `ee ff`.
- A lidar configured for unicast MSOP (a fixed destination host) only shows up on that host; run the tool there.

## Reboot a lidar

`reboot` sends the SDK2 reboot command (`cmd 0x0200`) to the lidar's command port — the cable-free way to make a saved IP change take effect:

```text
$ livox-discover reboot -lidar 192.168.1.120
[reboot] 192.168.1.120:56100  delay=100  (from :56101)
[reboot] attempt 1/3: sent 28 bytes
[ack] from 192.168.1.120  ret_code=0
[reboot] success: 192.168.1.120 is restarting; a Mid360 boots in well under a minute.
```

Flags: `-lidar` (required), `-cmd-port` (default 56100), `-delay` (default 100, the value the SDK sends). Exit codes match `set-ip`.

## Web UI

`web` serves the same capabilities in a browser — handy when a CLI is awkward, or from a tablet next to the lidar:

```bash
livox-discover web                        # http://127.0.0.1:8080
livox-discover web -listen 0.0.0.0:8080   # open the UI to the network
```

The page has a scan panel (duration + active-probe toggle), the discovered-lidar table, and an ops panel that prefills from the selected lidar: change IP and reboot with one click, cross-subnet hints included. Scan progress streams over SSE (`GET /api/events`, pushed only on change) with `GET /api/scan` as a plain REST fallback; errors carry stable `LVX-*` codes with readable messages. The web UI currently drives Livox lidars; Hesai lidars run through the CLI commands above.

| Endpoint | Purpose |
|---|---|
| `POST /api/scan` `{duration_seconds, probe}` | start a background scan (202; 409 while one runs) |
| `GET /api/scan` | current scan state incl. interim results |
| `POST /api/scan/stop` | stop the running scan early |
| `GET /api/events` | SSE stream of scan state |
| `POST /api/set-ip` `{lidar, ip, mask, gw, cmd_port}` | change a lidar's IP |
| `POST /api/reboot` `{lidar, cmd_port, delay}` | restart a lidar |

The frontend is a Vue 3 app embedded into the binary — `web/dist` is committed (~80 KB), so `go install` needs no node toolchain. To develop it: `cd web && npm install && npm run dev` (proxies `/api` to a `livox-discover web` instance on 127.0.0.1:8080), then `npm run build` and commit the refreshed `dist` together with the source changes.

## Cross-subnet lidars

The lidar's discovery traffic is link-layer broadcast, so as long as it hangs on the same L2 network the tool can see it even when its IP sits in a different subnet than your NIC (factory default `192.168.144.1xx` vs your `192.168.31.x`, say). Caveats and helpers:

- Not every lidar talks unprompted — the Mid360S under test never broadcast on its own and ignored queries from ephemeral source ports. It answered only queries sent **from source port 56000**, exactly how the SDK emits them. `-probe` therefore reuses the tool's 56000 listener as its query source and broadcasts one query per second to every local subnet's directed broadcast address; replies are reported like any other hit and the loopback copies of the tool's own broadcasts are filtered out.
- On Linux, strict reverse-path filtering (`net.ipv4.conf.all.rp_filter = 1`) can drop cross-subnet packets as martians; relax it for the lidar-facing interface if the tool stays silent while `tcpdump` still shows traffic.
- When a discovered lidar is outside every local subnet, the summary prints `[hint]` lines with the exact `ifconfig`/`ip` commands that give this host a path to it — unicast tools (drivers, `set-ip`) need one of them. `set-ip` prints the same advice when a lidar never answers.

## How it works

Livox SDK2 devices answer discovery queries with UDP packets that use the SDK2 command frame format, and many broadcast them periodically even when nobody asks. The tool passively binds the SDK2 broadcast/push ports with `SO_REUSEADDR`, counts every sender it observes, and fully validates + decodes every discovery ACK; with `-probe` it additionally broadcasts queries itself, ignoring the loopback copies of its own broadcasts.

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

### Hesai side

Hesai lidars have no queryable discovery; instead the MSOP point-cloud stream gives them away. MSOP packets of the JT128/AT128 family are 1100 bytes, start with the `ee ff` start-of-block marker followed by a protocol version (a captured JT128 sends `ee ff 01 04 ...` from source port 10000 to UDP 2368, broadcast from factory defaults). The marker byte order alone separates them from Velodyne packets on the same port (`ff ee`).

Configuration rides on PTC version 1.0 over TCP 9347 (facts from the BSD-licensed [HesaiLidar_SDK_2.0](https://github.com/HesaiTechnology/HesaiLidar_SDK_2.0), field-verified on a JT128). Frame layout:

| Offset | Size | Field |
|---|---|---|
| 0 | 1 | protocol identifier, always `0x47` |
| 1 | 1 | protocol identifier, always `0x74` |
| 2 | 1 | command code (`0x05` read calibration, `0x10` reboot, `0x21` set net) |
| 3 | 1 | return code, `0` in requests |
| 4 | 4 | payload length, big-endian |
| 8 | … | payload |

No checksums. A response repeats the request's command code; a non-zero return code is a rejection. The `set net` payload is 15 bytes: `ip[4] mask[4] gw[4]` as dotted quads, a VLAN flag byte and a big-endian `u16` VLAN id. The client resynchronizes on the `0x47 0x74` marker before parsing a response header, mirroring the SDK's header search.

## Usage notes

- A source that shows up on port 56000 with `0xAA`-headed packets is an SDK2 lidar; sources that never appear are either powered off, on another subnet (data-plane is unicast and must be same-subnet), or filtered.
- Run it on the host that the lidar sends data to (same NIC/subnet as configured in `host_net_info`).
- `-ports` accepts any comma-separated list, e.g. `-ports 56000` for a focused scan or a high port for testing.
- Mind the two port sides: the lidar listens on `56x00` (56100–56500) but sends to the host on `56x01` plus the 56000 discovery broadcast. Listening on lidar-side ports receives nothing; the tool prints a warning when `-ports` contains them.
- Silence on every port usually means the lidar is in another subnet or only answers queries: try `-probe`, and check the `[hint]` lines the summary prints for cross-subnet lidars.

## Development

```bash
gofmt -l .
go vet ./...
go test ./...
```

The frame/CRC parsers and the key-value control payload live in `internal/livox` and are covered by table-driven tests, including byte-exact fixtures generated by a reference implementation of the protocol. The `set-ip` exchange is tested end-to-end against a fake lidar on loopback. The Hesai MSOP signature and PTC client live in `internal/hesai`, tested the same way — including a fake PTC lidar that prefixes stray bytes to exercise the header resynchronization.

## Contributing

Issues and PRs are welcome. See [LICENSE](LICENSE) (Apache-2.0).
