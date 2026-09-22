package mesh

import (
	"encoding/binary"
	"net"
	"net/netip"
	"time"
)

// ServeSTUN serves only RFC 5389 IPv4 Binding requests until pc is closed.
// It discovers public endpoints; it is not TURN and never forwards user data.
// The owner creates/closes the UDP listener (typically UDP 3478). Responses are
// globally capped at 200 per second and at 20 per source IP per second.
func ServeSTUN(pc net.PacketConn) error {
	packet := make([]byte, 2048)
	window := time.Now()
	counts := make(map[netip.Addr]int)
	total := 0
	for {
		size, source, err := pc.ReadFrom(packet)
		if err != nil {
			return err
		}
		if size < 20 || binary.BigEndian.Uint16(packet[:2]) != 1 || binary.BigEndian.Uint32(packet[4:8]) != stunMagic || int(binary.BigEndian.Uint16(packet[2:4])) != size-20 || (size-20)%4 != 0 {
			continue
		}
		ap, err := netip.ParseAddrPort(source.String())
		if err != nil {
			continue
		}
		ap = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
		if !ap.Addr().Is4() || ap.Port() == 0 {
			continue
		}
		now := time.Now()
		if now.Sub(window) >= time.Second {
			window = now
			clear(counts)
			total = 0
		}
		if total >= 200 || counts[ap.Addr()] >= 20 {
			continue
		}
		total++
		counts[ap.Addr()]++
		response := make([]byte, 32)
		binary.BigEndian.PutUint16(response[:2], 0x0101)
		binary.BigEndian.PutUint16(response[2:4], 12)
		binary.BigEndian.PutUint32(response[4:8], stunMagic)
		copy(response[8:20], packet[8:20])
		binary.BigEndian.PutUint16(response[20:22], 0x0020)
		binary.BigEndian.PutUint16(response[22:24], 8)
		response[25] = 1
		binary.BigEndian.PutUint16(response[26:28], ap.Port()^uint16(stunMagic>>16))
		ip := ap.Addr().As4()
		binary.BigEndian.PutUint32(response[28:32], binary.BigEndian.Uint32(ip[:])^stunMagic)
		// A transient send failure to one client must not stop the shared service.
		pc.WriteTo(response, source)
	}
}
