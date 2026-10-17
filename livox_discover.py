#!/usr/bin/env python3
"""Livox 雷达发现工具：监听 SDK2 广播/推送端口，打印来包源 IP（= 雷达真实 IP）。

用途：网络上有多台 Livox 设备 / 出厂配置里的雷达 IP 不对时，
用本工具 15 秒找出真正在广播的雷达 IP，再填进驱动 JSON 的 lidar_configs[].ip。

用法（机器人上，先停掉占用端口的驱动）：
    pkill -f livox_ros_driver2_nod[e]; pkill -f livox_lidar_nod[e]
    python3 livox_discover.py 15

判读：
    - Livox SDK2 协议包以 0xaa 开头，出现在 56000（广播）/ 56200→56201（推送）
    - summary 里 src IP 即雷达地址（例如 192.0.2.10）。
"""
import socket
import sys
import time

PORTS = [55000, 56000, 56201]


def main(timeout_s: float = 15.0) -> None:
    socks = []
    for port in PORTS:
        try:
            s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            s.bind(("0.0.0.0", port))
            s.settimeout(1.0)
            socks.append((port, s))
            print(f"[listen] 0.0.0.0:{port}")
        except OSError as e:
            print(f"[skip]   port {port}: {e}")
    if not socks:
        print("no ports available")
        return
    print(f"listening {timeout_s}s ...")
    seen: dict[tuple[str, int, int], int] = {}
    deadline = time.time() + timeout_s
    while time.time() < deadline:
        for port, s in socks:
            try:
                data, addr = s.recvfrom(2048)
            except socket.timeout:
                continue
            key = (addr[0], addr[1], port)
            seen[key] = seen.get(key, 0) + 1
            if seen[key] in (1, 50, 500):
                head = " ".join(f"{b:02x}" for b in data[:16])
                print(f"[hit] src={addr[0]}:{addr[1]} -> local:{port}  len={len(data)}  head={head}  cnt={seen[key]}")
    print("=== summary ===")
    for (ip, sport, lport), cnt in sorted(seen.items()):
        print(f"src {ip}:{sport} -> local:{lport}  packets={cnt}")


if __name__ == "__main__":
    main(float(sys.argv[1]) if len(sys.argv) > 1 else 15.0)
