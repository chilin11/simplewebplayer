package castnet

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestCastMessageWire(t *testing.T) {
	wire := []byte{0, 0, 0, 17, 8, 0, 18, 1, 's', 26, 1, 'r', 34, 1, 'n', 40, 0, 50, 2, '{', '}'}
	m, err := readFrame(bytes.NewReader(wire))
	if err != nil || m.source != "s" || m.dest != "r" || m.ns != "n" || !bytes.Equal(m.data, []byte("{}")) {
		t.Fatal("CastMessage fixture did not decode")
	}
	var out bytes.Buffer
	if err := writeFrame(&out, m); err != nil || !bytes.Equal(out.Bytes(), wire) {
		t.Fatal("unexpected CastMessage encoding")
	}
	for _, bad := range [][]byte{{0, 0, 0, 0}, {0, 0, 0, 2, 8, 0}, {0, 0, 0, 1, 0xff}} {
		if _, err := readFrame(bytes.NewReader(bad)); !errors.Is(err, errFrame) {
			t.Fatal("accepted invalid frame")
		}
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], maxFrame+1)
	if _, err := readFrame(bytes.NewReader(size[:])); !errors.Is(err, errFrame) {
		t.Fatal("oversize frame")
	}
	if _, err := readFrame(bytes.NewReader(wire[:len(wire)-1])); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal("short frame")
	}
}
