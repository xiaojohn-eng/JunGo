package openvpn

import "errors"

const (
	lzoCompressNone = 0xFA // not compressed
	lzoCompressLZO  = 0x66 // LZO compressed
)

var ErrLZODecompress = errors.New("lzo decompression failed")

// lzo1xDecompressSafe handles OpenVPN comp-lzo data packets.
// First byte is the compression header, followed by the payload.
func lzo1xDecompressSafe(src []byte) ([]byte, error) {
	if len(src) == 0 {
		return nil, ErrLZODecompress
	}

	switch src[0] {
	case lzoCompressNone:
		if len(src) > 1 {
			return src[1:], nil
		}
		return nil, nil
	case lzoCompressLZO:
		// LZO decoding is deliberately unavailable. A config requesting it is
		// rejected by Prepare, and an unexpected compressed packet fails closed.
		return nil, ErrLZODecompress
	default:
		return nil, ErrLZODecompress
	}
}

// lzo1xCompressSafe handles OpenVPN comp-lzo data packets.
// Prepend comp-lzo header (0xfa = not compressed) to satisfy servers expecting the framing.
func lzo1xCompressSafe(src []byte) ([]byte, error) {
	lzoPacket := make([]byte, 1+len(src))
	lzoPacket[0] = lzoCompressNone
	copy(lzoPacket[1:], src)
	return lzoPacket, nil
}
