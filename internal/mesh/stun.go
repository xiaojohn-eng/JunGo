package mesh

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

const stunMagic uint32 = 0x2112a442

type stunRequest struct {
	server netip.AddrPort
	result chan netip.AddrPort
}

func (b *meshBind) discoverSTUN(ctx context.Context, server string) (string, error) {
	ap, err := netip.ParseAddrPort(server)
	if err != nil || !ap.Addr().Is4() || ap.Port() == 0 || ap.Addr().IsUnspecified() || ap.Addr().IsMulticast() {
		return "", errors.New("STUN server must be a numeric IPv4 address:port")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var tx [12]byte
	if _, err = rand.Read(tx[:]); err != nil {
		return "", err
	}
	result := make(chan netip.AddrPort, 1)
	b.mu.Lock()
	s := b.session
	if b.stopped || s == nil {
		b.mu.Unlock()
		return "", net.ErrClosed
	}
	if b.forceRelay {
		b.mu.Unlock()
		return "", errors.New("STUN disabled while ForceRelay is enabled")
	}
	b.stun[tx] = stunRequest{server: ap, result: result}
	b.mu.Unlock()
	defer func() { b.mu.Lock(); delete(b.stun, tx); b.mu.Unlock() }()
	packet := make([]byte, 20)
	binary.BigEndian.PutUint16(packet[0:2], 1)
	binary.BigEndian.PutUint32(packet[4:8], stunMagic)
	copy(packet[8:20], tx[:])
	if _, err = s.udp.WriteToUDPAddrPort(packet, ap); err != nil {
		return "", err
	}
	select {
	case mapped := <-result:
		return mapped.String(), nil
	case <-ctx.Done():
		return "", fmt.Errorf("STUN mapping: %w", ctx.Err())
	case <-s.done:
		return "", net.ErrClosed
	}
}

func (b *meshBind) handleSTUN(packet []byte, source netip.AddrPort) bool {
	if len(packet) < 20 || binary.BigEndian.Uint32(packet[4:8]) != stunMagic {
		return false
	}
	var tx [12]byte
	copy(tx[:], packet[8:20])
	b.mu.Lock()
	request, ok := b.stun[tx]
	b.mu.Unlock()
	if !ok || request.server != source {
		return true
	}
	ap, err := parseSTUNMapping(packet)
	if err != nil {
		return true
	}
	select {
	case request.result <- ap:
	default:
	}
	return true
}

func parseSTUNMapping(packet []byte) (netip.AddrPort, error) {
	if len(packet) < 20 || binary.BigEndian.Uint16(packet[:2]) != 0x0101 || binary.BigEndian.Uint32(packet[4:8]) != stunMagic || int(binary.BigEndian.Uint16(packet[2:4])) != len(packet)-20 {
		return netip.AddrPort{}, errors.New("invalid STUN binding response")
	}
	for pos := 20; pos < len(packet); {
		if pos+4 > len(packet) {
			break
		}
		kind := binary.BigEndian.Uint16(packet[pos : pos+2])
		size := int(binary.BigEndian.Uint16(packet[pos+2 : pos+4]))
		pos += 4
		if pos+size > len(packet) {
			break
		}
		if kind == 0x0020 && size == 8 && packet[pos+1] == 1 {
			port := binary.BigEndian.Uint16(packet[pos+2:pos+4]) ^ uint16(stunMagic>>16)
			ip := binary.BigEndian.Uint32(packet[pos+4:pos+8]) ^ stunMagic
			var bytes [4]byte
			binary.BigEndian.PutUint32(bytes[:], ip)
			addr := netip.AddrFrom4(bytes)
			if port == 0 || addr.IsUnspecified() || addr.IsMulticast() {
				break
			}
			return netip.AddrPortFrom(addr, port), nil
		}
		pos += (size + 3) &^ 3
	}
	return netip.AddrPort{}, errors.New("STUN response has no valid IPv4 XOR-MAPPED-ADDRESS")
}
