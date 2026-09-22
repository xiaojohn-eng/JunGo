package relay

import "testing"

func BenchmarkPacketFraming(b *testing.B) {
	payload := make([]byte, 1400)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := EncodePacket("abcdefghijklmnopqrstuv", payload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPacketFramingReused(b *testing.B) {
	payload := make([]byte, 1400)
	frame := make([]byte, MaxFrame)
	b.ReportAllocs()
	for b.Loop() {
		var err error
		frame, err = encodePacket(frame, "abcdefghijklmnopqrstuv", payload)
		if err != nil {
			b.Fatal(err)
		}
	}
}
