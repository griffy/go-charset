package charset
import (
	"encoding/binary"
	"os"
	"utf8"
)

type utf16CvtToUTF8 struct {
	first bool
	endian binary.ByteOrder
	scratch []byte
}

func (p *utf16CvtToUTF8) Convert(data []byte, eof bool) (int, []byte, os.Error) {
	data = data[0:len(data)&^1]	// round to even number of bytes.
	if len(data) < 2 {
		return 0, nil, nil
	}
	n := 0
	if p.first && p.endian == nil {
		switch binary.BigEndian.Uint16(data) {
		case 0xfeff:
			p.endian = binary.BigEndian
			data = data[2:]
			n += 2
		case 0xfffe:
			p.endian = binary.LittleEndian
			data = data[2:]
			n += 2
		default:
			p.endian = guessEndian(data)
		}
		p.first = false
	}

	p.scratch = p.scratch[:0]
	for ; len(data) > 0; data = data[2:] {
		p.scratch = appendRune(p.scratch, int(p.endian.Uint16(data)))
		n += 2
	}
	return n, p.scratch, nil
}

func guessEndian(data []byte) binary.ByteOrder {
	// XXX TODO
	return binary.LittleEndian
}

type utf16CvtFromUTF8 struct {
	first bool
	endian binary.ByteOrder
	scratch []byte
}

func (p *utf16CvtFromUTF8) Convert(data []byte, eof bool) (int, []byte, os.Error) {
	p.scratch = ensure(p.scratch[:0], (len(data) + 1) * 2)
	if p.first {
		p.scratch = p.scratch[0:2]
		p.endian.PutUint16(p.scratch, 0xfeff)
		p.first = false
	}
	n := 0
	for len(data) > 0 {
		if !utf8.FullRune(data) && !eof {
			break
		}
		r, size := utf8.DecodeRune(data)
		// TODO if r > 65535?

		slen := len(p.scratch)
		p.scratch = p.scratch[0: slen+2]
		p.endian.PutUint16(p.scratch[slen:], uint16(r))
		data = data[size:]
		n += size
	}
	return n, p.scratch, nil
}

func getEndian(arg string) (binary.ByteOrder, os.Error) {
	switch arg {
	case "le":
		return binary.LittleEndian, nil
	case "be":
		return binary.BigEndian, nil
	case "":
		return nil, nil
	}
	return nil, os.ErrorString("charset: unknown utf16 endianness")
}
	
func utf16ToUTF8(arg string) (func() Converter, os.Error) {
	endian, err := getEndian(arg)
	if err != nil {
		return nil, err
	}

	return func() Converter {
		return &utf16CvtToUTF8{first: true, endian: endian}
	}, nil
}
	
func utf16FromUTF8(arg string) (func() Converter, os.Error) {
	endian, err := getEndian(arg)
	if err != nil {
		return nil, err
	}

	return func() Converter {
		return &utf16CvtFromUTF8{first: true, endian: endian}
	}, nil
}
