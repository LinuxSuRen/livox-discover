// Package hesai implements what this tool needs to find and manage Hesai
// lidars (JT128 / AT128 family): recognizing the MSOP point-cloud packets a
// Hesai lidar streams to UDP 2368, and a minimal client for PTC, the TCP
// control protocol the Hesai SDK uses to configure a lidar — set its IP,
// reboot it.
//
// The protocol facts below were taken from the BSD-licensed
// HesaiLidar_SDK_2.0 (libhesai PtcClient/PtcParser sources) and verified
// against a real JT128 on a robot sensor network.
package hesai

// MSOPPort is the UDP port Hesai lidars stream their MSOP point-cloud
// packets to. Factory-default units broadcast 1100-byte packets there from
// source port 10000, which is what passive discovery listens for.
const MSOPPort = 2368

// MSOPPacketSize is the datagram size of the JT128/AT128 MSOP packets;
// reported for context, not enforced — other Hesai models use other sizes.
const MSOPPacketSize = 1100

// IsMSOP reports whether data looks like a Hesai MSOP point-cloud packet.
// Hesai MSOP packets start with the 0xEE 0xFF start-of-block marker
// (verified on a JT128: 1100 bytes, EE FF 01 04 ...). Velodyne lidars,
// which also use port 2368, start their data packets with 0xFF 0xEE — the
// byte order alone tells the families apart.
func IsMSOP(data []byte) bool {
	return len(data) >= 2 && data[0] == 0xEE && data[1] == 0xFF
}
