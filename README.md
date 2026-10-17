# livox-discover

Zero-dependency single-file CLI that finds **Livox SDK2 lidars** (Mid360 / Mid360S / Avia / Horizon ...) on your network in seconds, by listening to their UDP broadcast/push ports.

## Why

Livox SDK2 has no standalone discovery CLI. Common pain it solves:

- The lidar's factory IP does not match the `lidar_configs[].ip` in your driver JSON — the driver silently receives nothing;
- Multiple Livox units are on the network and you need the real IP of each;
- You want to see which units are actually broadcasting before digging into driver configs.

## Quick start

Requires Python 3.8+ with **no third-party dependencies** (stdlib only):

```bash
python3 livox_discover.py 15
```

Stop any driver that occupies the ports first (e.g. `pkill -f livox_lidar_nod[e]`), then run the tool. Sample output:

```text
[listen] 0.0.0.0:55000
[listen] 0.0.0.0:56000
[listen] 0.0.0.0:56201
listening 15.0s ...
[hit] src=192.0.2.10:65000 -> local:56000  len=60  head=aa 01 ...  cnt=1
=== summary ===
src 192.0.2.10:65000 -> local:56000  packets=15
```

The `src` IP in the summary is the lidar's address — paste it into the `ip` field of your driver config.

## How it works

Livox SDK2 devices periodically answer discovery queries by broadcasting UDP packets that start with the `0xaa` protocol header. The tool binds the SDK2 broadcast/push ports (55000 / 56000 / 56201) with `SO_REUSEADDR`, prints every sender it observes, and summarizes per-source packet counts. Ports and header facts come from the public SDK2 protocol.

## Usage notes

- A source that shows up on port 56000 with `0xaa`-headed packets is an SDK2 lidar; sources that never appear are either powered off, on another subnet (data-plane is unicast and must be same-subnet), or filtered.
- Run it on the host that the lidar sends data to (same NIC/subnet as configured in `host_net_info`).
- The argument is the listen duration in seconds (default 15).

## Contributing

Issues and PRs are welcome. See [LICENSE](LICENSE) (Apache-2.0).
