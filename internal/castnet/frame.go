package castnet

import (
	"encoding/binary"
	"errors"
	"io"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
)

const maxFrame = 64 << 10

var errFrame = errors.New("cast: invalid channel frame")

// CastMessage (cast_channel.proto): 4-byte network-order size followed by
// proto2 fields 1 protocol_version, 2 source_id, 3 destination_id, 4 namespace,
// 5 payload_type, 6 payload_utf8 OR 7 payload_binary. We send only v1.0 JSON.
type envelope struct {
	source, dest, ns string
	data             []byte
	binary           bool
}

func writeFrame(w io.Writer, m envelope) error {
	if len(m.data) > maxFrame || len(m.source) > 256 || len(m.dest) > 256 || len(m.ns) > 256 || m.binary {
		return errFrame
	}
	p := make([]byte, 4, 4+len(m.source)+len(m.dest)+len(m.ns)+len(m.data)+32)
	p = protowire.AppendTag(p, 1, protowire.VarintType)
	p = protowire.AppendVarint(p, 0)
	for i, s := range []string{m.source, m.dest, m.ns} {
		p = protowire.AppendTag(p, protowire.Number(i+2), protowire.BytesType)
		p = protowire.AppendString(p, s)
	}
	p = protowire.AppendTag(p, 5, protowire.VarintType)
	p = protowire.AppendVarint(p, 0)
	p = protowire.AppendTag(p, 6, protowire.BytesType)
	p = protowire.AppendBytes(p, m.data)
	if len(p)-4 > maxFrame {
		return errFrame
	}
	binary.BigEndian.PutUint32(p, uint32(len(p)-4))
	n, err := w.Write(p)
	if err == nil && n != len(p) {
		return io.ErrShortWrite
	}
	return err
}

func readFrame(r io.Reader) (envelope, error) {
	var sz [4]byte
	if _, err := io.ReadFull(r, sz[:]); err != nil {
		return envelope{}, err
	}
	n := binary.BigEndian.Uint32(sz[:])
	if n == 0 || n > maxFrame {
		return envelope{}, errFrame
	}
	p := make([]byte, int(n))
	if _, err := io.ReadFull(r, p); err != nil {
		return envelope{}, err
	}
	return decodeFrame(p)
}

func decodeFrame(p []byte) (envelope, error) {
	var m envelope
	var seen uint8
	for fields := 0; len(p) > 0; fields++ {
		k, t, n := protowire.ConsumeTag(p)
		if fields >= 64 || n < 0 {
			return envelope{}, errFrame
		}
		p = p[n:]
		if k >= 1 && k <= 7 {
			bit := uint8(1) << k
			if seen&bit != 0 {
				return envelope{}, errFrame
			}
			seen |= bit
		}
		switch k {
		case 1, 5:
			v, n := protowire.ConsumeVarint(p)
			if t != protowire.VarintType || n < 0 || k == 1 && v != 0 || k == 5 && v > 1 {
				return envelope{}, errFrame
			}
			p = p[n:]
			if k == 5 {
				m.binary = v == 1
			}
		case 2, 3, 4, 6, 7:
			v, n := protowire.ConsumeBytes(p)
			if t != protowire.BytesType || n < 0 {
				return envelope{}, errFrame
			}
			p = p[n:]
			if k != 7 && !utf8.Valid(v) || k <= 4 && (len(v) == 0 || len(v) > 256) {
				return envelope{}, errFrame
			}
			switch k {
			case 2:
				m.source = string(v)
			case 3:
				m.dest = string(v)
			case 4:
				m.ns = string(v)
			case 6, 7:
				m.data = v
			}
		default:
			if t == protowire.StartGroupType || t == protowire.EndGroupType {
				return envelope{}, errFrame
			}
			n := protowire.ConsumeFieldValue(k, t, p)
			if n < 0 {
				return envelope{}, errFrame
			}
			p = p[n:]
		}
	}
	mask := uint8(0x7e)
	if m.binary {
		mask = 0xbe
	}
	if seen != mask {
		return envelope{}, errFrame
	}
	return m, nil
}
