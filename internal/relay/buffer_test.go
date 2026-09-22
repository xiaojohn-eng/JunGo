package relay

import (
	"bytes"
	"strings"
	"testing"
)

func TestReframePreservesCiphertextAndAuthenticatedID(t *testing.T) {
	for _, oldID := range []string{"a", strings.Repeat("b", 22), strings.Repeat("c", 128)} {
		for _, newID := range []string{"x", strings.Repeat("y", 22), strings.Repeat("z", 128)} {
			for _, size := range []int{1, 1400, MaxPayload} {
				payload := bytes.Repeat([]byte{0xa5}, size)
				original, _ := EncodePacket(oldID, payload)
				for _, spareCapacity := range []bool{false, true} {
					input := make([]byte, len(original))
					if spareCapacity {
						input = make([]byte, len(original), MaxFrame)
					}
					copy(input, original)
					_, alias, err := DecodePacket(input)
					if err != nil {
						t.Fatal(err)
					}
					output, err := encodePacket(input, newID, alias)
					if err != nil {
						t.Fatal(err)
					}
					gotID, gotPayload, err := DecodePacket(output)
					if err != nil || gotID != newID || !bytes.Equal(gotPayload, payload) {
						t.Fatalf("reframing corrupted packet: old=%d new=%d size=%d spare=%v", len(oldID), len(newID), size, spareCapacity)
					}
				}
			}
		}
	}
}
