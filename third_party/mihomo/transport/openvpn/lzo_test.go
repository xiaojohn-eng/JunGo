package openvpn

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
)

func TestLzo1xSafe(t *testing.T) {
	data := make([]byte, 1024)
	rand.Read(data)
	compressed, err := lzo1xCompressSafe(data)
	if err != nil {
		t.Fatal(err)
	}
	decompressed, err := lzo1xDecompressSafe(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, decompressed) {
		t.Fatal("decompressed data is not equal to original")
	}
}

func TestLzo1xRejectsCompressedPacket(t *testing.T) {
	for _, packet := range [][]byte{{lzoCompressLZO}, {lzoCompressLZO, 0x11, 0x22}, {0x00}, nil} {
		if _, err := lzo1xDecompressSafe(packet); !errors.Is(err, ErrLZODecompress) {
			t.Fatalf("packet %x: expected LZO rejection, got %v", packet, err)
		}
	}
}
