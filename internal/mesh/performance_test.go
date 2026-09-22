package mesh

import (
	"net"
	"net/netip"
	"testing"
)

type discardRelay struct{}

func (discardRelay) Send(string, []byte) error        { return nil }
func (discardRelay) Receive() (string, []byte, error) { return "", nil, net.ErrClosed }
func (discardRelay) Close() error                     { return nil }

func BenchmarkBindRelaySend(b *testing.B) {
	bind := &meshBind{relay: discardRelay{}, session: &bindSession{}, peers: map[string]*bindPeer{"peer": {candidates: []netip.AddrPort{netip.MustParseAddrPort("192.168.1.2:51820"), netip.MustParseAddrPort("1.2.3.4:51820")}}}}
	ep := &meshEndpoint{peerID: "peer"}
	packets := [][]byte{make([]byte, 1400)}
	b.ReportAllocs()
	for b.Loop() {
		if err := bind.Send(packets, ep); err != nil {
			b.Fatal(err)
		}
	}
}
