// Package jmsengine implements a minimal AMQP 1.0 peer — enough of RFC-like
// OASIS AMQP 1.0 (protocol header exchange, open/begin/attach/flow/
// transfer/disposition/detach/end/close, no SASL/auth, no transactions, no
// link resumption) for a real AMQP 1.0 client library to connect, send a
// message, and receive one back. AMQP 1.0 is what this engine mocks JMS
// with: it's the wire protocol real JMS providers (Qpid JMS, ActiveMQ
// Artemis's AMQP connector) actually speak, unlike the classic ActiveMQ-
// specific OpenWire protocol, for which no Go implementation — client or
// server — exists at all to build on or verify against.
//
// Unlike the Kafka/SMPP/Diameter engines, this one has no third-party
// codec to depend on: every published Go AMQP 1.0 library keeps its wire
// encode/decode machinery under an internal/ package, which Go's import
// rules make unusable outside that library's own module. What's here
// instead is hand-rolled directly from the AMQP 1.0 type system and
// performative definitions — cross-checked line-for-line against
// github.com/Azure/go-amqp's own (unimportable but readable) internal
// frame definitions to pin down exact field order, defaults, and mandatory
// flags, then verified end-to-end against that same library's real client
// (see engine_test.go) rather than trusting the spec reading alone.
package jmsengine

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Symbol is an AMQP "symbol" value — an ASCII string used for names/codes
// (e.g. capabilities, distribution modes) — kept as a distinct type from
// string so a decoded value's original AMQP type can still be told apart
// where it matters.
type Symbol string

// described represents any AMQP "described type": a descriptor value (here
// always a small non-negative integer for the fixed codes this engine
// cares about) followed by the type's actual value.
type described struct {
	Descriptor uint64
	Value      any
}

var errShortBuffer = errors.New("jms: unexpected end of data")

// decoder reads AMQP values one at a time from an in-memory byte slice —
// every performative and message body this engine ever needs to parse
// fits comfortably in memory, so there's no need for a streaming reader.
type decoder struct {
	buf []byte
	pos int
}

func newDecoder(buf []byte) *decoder { return &decoder{buf: buf} }

func (d *decoder) remaining() int { return len(d.buf) - d.pos }

func (d *decoder) readByte() (byte, error) {
	if d.remaining() < 1 {
		return 0, errShortBuffer
	}
	b := d.buf[d.pos]
	d.pos++
	return b, nil
}

func (d *decoder) readN(n int) ([]byte, error) {
	if n < 0 || d.remaining() < n {
		return nil, errShortBuffer
	}
	b := d.buf[d.pos : d.pos+n]
	d.pos += n
	return b, nil
}

func (d *decoder) readUint(n int) (uint64, error) {
	b, err := d.readN(n)
	if err != nil {
		return 0, err
	}
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v, nil
}

// readValue decodes exactly one AMQP value (primitive, compound, or
// described) starting at the current position.
func (d *decoder) readValue() (any, error) {
	code, err := d.readByte()
	if err != nil {
		return nil, err
	}
	switch code {
	case 0x40: // null
		return nil, nil
	case 0x41: // boolean true (compact)
		return true, nil
	case 0x42: // boolean false (compact)
		return false, nil
	case 0x56: // boolean (1 byte)
		b, err := d.readByte()
		return b != 0, err
	case 0x50, 0x52, 0x53: // ubyte, smalluint, smallulong — all: 1 byte -> uint64
		return d.readUint(1)
	case 0x60: // ushort
		return d.readUint(2)
	case 0x70: // uint
		return d.readUint(4)
	case 0x80: // ulong
		return d.readUint(8)
	case 0x43, 0x44: // uint0, ulong0
		return uint64(0), nil
	case 0x51, 0x54, 0x55: // byte, smallint, smalllong — all: 1 byte signed -> int64
		return d.readSigned(1)
	case 0x61: // short
		return d.readSigned(2)
	case 0x71: // int
		return d.readSigned(4)
	case 0x81: // long
		return d.readSigned(8)
	case 0x72: // float
		v, err := d.readUint(4)
		return float64(math.Float32frombits(uint32(v))), err
	case 0x82: // double
		v, err := d.readUint(8)
		return math.Float64frombits(v), err
	case 0x83: // timestamp (ms since epoch)
		v, err := d.readUint(8)
		return int64(v), err
	case 0x74, 0x84, 0x94: // decimal32/64/128 — not interpreted, kept as raw bytes
		n := map[byte]int{0x74: 4, 0x84: 8, 0x94: 16}[code]
		return d.readN(n)
	case 0x73: // char (UTF-32BE code point)
		v, err := d.readUint(4)
		return rune(v), err
	case 0x98: // uuid
		return d.readN(16)
	case 0xa0, 0xb0, 0xa1, 0xb1, 0xa3, 0xb3: // vbin8/32, str8/32-utf8, sym8/32
		return d.readVariableWidth(code)
	case 0x45: // list0
		return []any{}, nil
	case 0xc0: // list8
		return d.readCompound(1, false)
	case 0xd0: // list32
		return d.readCompound(4, false)
	case 0xc1: // map8
		return d.readCompound(1, true)
	case 0xd1: // map32
		return d.readCompound(4, true)
	case 0xe0: // array8
		return d.readArray(1)
	case 0xf0: // array32
		return d.readArray(4)
	case 0x00: // described type
		return d.readDescribed()
	default:
		return nil, fmt.Errorf("jms: unsupported AMQP type code %#x", code)
	}
}

// readSigned reads n bytes as a two's-complement signed integer.
func (d *decoder) readSigned(n int) (int64, error) {
	v, err := d.readUint(n)
	if err != nil {
		return 0, err
	}
	switch n {
	case 1:
		return int64(int8(v)), nil
	case 2:
		return int64(int16(v)), nil
	case 4:
		return int64(int32(v)), nil
	default:
		return int64(v), nil
	}
}

// readVariableWidth reads the length-prefixed binary/string/symbol types —
// vbin8/32, str8/32-utf8, sym8/32 — which all share the same "N-byte
// length then that many bytes" shape and differ only in how those bytes
// are wrapped for the caller.
func (d *decoder) readVariableWidth(code byte) (any, error) {
	width := 1
	if code == 0xb0 || code == 0xb1 || code == 0xb3 {
		width = 4
	}
	n, err := d.readUint(width)
	if err != nil {
		return nil, err
	}
	b, err := d.readN(int(n))
	if err != nil {
		return nil, err
	}
	switch code {
	case 0xa1, 0xb1:
		return string(b), nil
	case 0xa3, 0xb3:
		return Symbol(b), nil
	default: // 0xa0, 0xb0
		return b, nil
	}
}

func (d *decoder) readDescribed() (any, error) {
	descAny, err := d.readValue()
	if err != nil {
		return nil, err
	}
	desc, ok := toUint64(descAny)
	if !ok {
		return nil, fmt.Errorf("jms: unsupported descriptor type %T", descAny)
	}
	val, err := d.readValue()
	if err != nil {
		return nil, err
	}
	return &described{Descriptor: desc, Value: val}, nil
}

// readCompound reads a list (asMap=false) or map (asMap=true): a
// widthBytes-wide size, a widthBytes-wide count, then that many values
// (twice that many for a map, alternating key/value).
func (d *decoder) readCompound(widthBytes int, asMap bool) (any, error) {
	size, err := d.readUint(widthBytes)
	if err != nil {
		return nil, err
	}
	count, err := d.readUint(widthBytes)
	if err != nil {
		return nil, err
	}
	_ = size // the values themselves are read positionally; size is redundant given count for our purposes
	if asMap {
		m := make(map[any]any, count/2)
		for i := uint64(0); i < count; i += 2 {
			k, err := d.readValue()
			if err != nil {
				return nil, err
			}
			v, err := d.readValue()
			if err != nil {
				return nil, err
			}
			m[k] = v
		}
		return m, nil
	}
	list := make([]any, 0, count)
	for i := uint64(0); i < count; i++ {
		v, err := d.readValue()
		if err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, nil
}

// readArray reads (and discards) an AMQP array. Unlike list/map, every
// element of an array shares one constructor (type code) written once up
// front rather than each element carrying its own — parsing that properly
// isn't worth it since nothing this engine does ever produces or inspects
// an array's contents; the only requirement is correctly skipping PAST one
// a real client sends (e.g. as an empty offered-capabilities field). The
// declared size already covers everything after it (the count field plus
// every element's bytes, per the same convention list/map use), so the
// whole thing can be skipped as one opaque blob without decoding a single
// element.
func (d *decoder) readArray(widthBytes int) (any, error) {
	size, err := d.readUint(widthBytes)
	if err != nil {
		return nil, err
	}
	if _, err := d.readN(int(size)); err != nil {
		return nil, err
	}
	return []any{}, nil
}

func toUint64(v any) (uint64, bool) {
	switch n := v.(type) {
	case uint64:
		return n, true
	case int64:
		return uint64(n), true
	}
	return 0, false
}

// --- encoding ---

type encoder struct {
	buf []byte
}

func (e *encoder) writeByte(b byte) { e.buf = append(e.buf, b) }
func (e *encoder) write(b []byte)   { e.buf = append(e.buf, b...) }

func (e *encoder) writeUint(width int, v uint64) {
	b := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	e.write(b)
}

func (e *encoder) writeNull() { e.writeByte(0x40) }

func (e *encoder) writeBool(v bool) {
	if v {
		e.writeByte(0x41)
	} else {
		e.writeByte(0x42)
	}
}

// writeULong encodes a ulong using the most compact form available — used
// both for plain ulong fields and, doubling as WriteDescriptor, for the
// small numeric codes (0x10-0x18, 0x28, 0x29) every performative and
// source/target descriptor in this engine uses.
func (e *encoder) writeULong(v uint64) {
	if v == 0 {
		e.writeByte(0x44)
		return
	}
	if v <= 0xff {
		e.writeByte(0x53)
		e.writeUint(1, v)
		return
	}
	e.writeByte(0x80)
	e.writeUint(8, v)
}

func (e *encoder) writeUint32(v uint32) {
	if v == 0 {
		e.writeByte(0x43)
		return
	}
	if v <= 0xff {
		e.writeByte(0x52)
		e.writeUint(1, uint64(v))
		return
	}
	e.writeByte(0x70)
	e.writeUint(4, uint64(v))
}

func (e *encoder) writeUint16(v uint16) {
	e.writeByte(0x60)
	e.writeUint(2, uint64(v))
}

func (e *encoder) writeString(s string) {
	if len(s) <= 0xff {
		e.writeByte(0xa1)
		e.writeUint(1, uint64(len(s)))
	} else {
		e.writeByte(0xb1)
		e.writeUint(4, uint64(len(s)))
	}
	e.write([]byte(s))
}

func (e *encoder) writeSymbol(s Symbol) {
	if len(s) <= 0xff {
		e.writeByte(0xa3)
		e.writeUint(1, uint64(len(s)))
	} else {
		e.writeByte(0xb3)
		e.writeUint(4, uint64(len(s)))
	}
	e.write([]byte(s))
}

func (e *encoder) writeBinary(b []byte) {
	if len(b) <= 0xff {
		e.writeByte(0xa0)
		e.writeUint(1, uint64(len(b)))
	} else {
		e.writeByte(0xb0)
		e.writeUint(4, uint64(len(b)))
	}
	e.write(b)
}

// writeDescriptor writes the "0x00 <descriptor-ulong>" prefix common to
// every described type this engine emits (performatives, source/target,
// message sections).
func (e *encoder) writeDescriptor(code uint64) {
	e.writeByte(0x00)
	e.writeULong(code)
}

// field is one positional element of a described list — pre-encoded bytes,
// or nil to mean "omit" (trailing omits shrink the list; a non-trailing
// omit is encoded as null to hold its position, exactly mirroring how
// every reference AMQP 1.0 implementation — including the one this was
// checked against — handles optional composite fields).
type field struct {
	bytes []byte // nil means omitted
}

func fieldOf(f func(e *encoder)) field {
	e := &encoder{}
	f(e)
	return field{bytes: e.buf}
}

func omittedField() field { return field{bytes: nil} }

// writeDescribedList writes a described composite type (a performative,
// source, or target): the descriptor, then a list32-encoded field array.
// Always uses the list32 form for simplicity — larger than strictly
// necessary for small lists, but unconditionally valid and exactly what
// this engine's own reference implementation does too.
func (e *encoder) writeDescribedList(code uint64, fields []field) {
	lastSet := -1
	for i, f := range fields {
		if f.bytes != nil {
			lastSet = i
		}
	}
	e.writeDescriptor(code)
	if lastSet == -1 {
		e.writeByte(0x45) // list0
		return
	}
	e.writeByte(0xd0) // list32
	sizeIdx := len(e.buf)
	e.write([]byte{0, 0, 0, 0}) // size placeholder
	countIdx := len(e.buf)
	e.write([]byte{0, 0, 0, 0}) // count placeholder
	contentStart := len(e.buf)
	for _, f := range fields[:lastSet+1] {
		if f.bytes == nil {
			e.writeNull()
		} else {
			e.write(f.bytes)
		}
	}
	count := uint32(lastSet + 1)
	size := uint32(len(e.buf)-contentStart) + 4 // +4 for the count field itself
	binary.BigEndian.PutUint32(e.buf[sizeIdx:], size)
	binary.BigEndian.PutUint32(e.buf[countIdx:], count)
}
