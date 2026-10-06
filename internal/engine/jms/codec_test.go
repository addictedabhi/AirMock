package jmsengine

import (
	"reflect"
	"testing"
)

func decodeOne(t *testing.T, buf []byte) any {
	t.Helper()
	d := newDecoder(buf)
	v, err := d.readValue()
	if err != nil {
		t.Fatalf("readValue: %v", err)
	}
	if d.remaining() != 0 {
		t.Fatalf("expected all bytes consumed, %d remain", d.remaining())
	}
	return v
}

func TestCodecPrimitivesRoundTrip(t *testing.T) {
	t.Run("null", func(t *testing.T) {
		e := &encoder{}
		e.writeNull()
		if got := decodeOne(t, e.buf); got != nil {
			t.Fatalf("expected nil, got %#v", got)
		}
	})
	t.Run("bool true/false", func(t *testing.T) {
		e := &encoder{}
		e.writeBool(true)
		e.writeBool(false)
		d := newDecoder(e.buf)
		v1, err := d.readValue()
		if err != nil || v1 != true {
			t.Fatalf("expected true, got %#v (%v)", v1, err)
		}
		v2, err := d.readValue()
		if err != nil || v2 != false {
			t.Fatalf("expected false, got %#v (%v)", v2, err)
		}
	})
	t.Run("string short and long", func(t *testing.T) {
		e := &encoder{}
		e.writeString("hello")
		if got := decodeOne(t, e.buf); got != "hello" {
			t.Fatalf("expected %q, got %#v", "hello", got)
		}
		long := make([]byte, 300)
		for i := range long {
			long[i] = 'x'
		}
		e2 := &encoder{}
		e2.writeString(string(long))
		if got := decodeOne(t, e2.buf); got != string(long) {
			t.Fatalf("long string round-trip mismatch: got len %d", len(got.(string)))
		}
	})
	t.Run("symbol", func(t *testing.T) {
		e := &encoder{}
		e.writeSymbol(Symbol("amqp:accepted:list"))
		got := decodeOne(t, e.buf)
		if got != Symbol("amqp:accepted:list") {
			t.Fatalf("expected symbol, got %#v", got)
		}
	})
	t.Run("binary", func(t *testing.T) {
		e := &encoder{}
		e.writeBinary([]byte("payload bytes"))
		got := decodeOne(t, e.buf)
		if !reflect.DeepEqual(got, []byte("payload bytes")) {
			t.Fatalf("expected binary round-trip, got %#v", got)
		}
	})
	t.Run("uint variants", func(t *testing.T) {
		for _, v := range []uint32{0, 42, 1_000_000} {
			e := &encoder{}
			e.writeUint32(v)
			got := decodeOne(t, e.buf)
			if got != uint64(v) {
				t.Fatalf("uint32(%d): expected %d, got %#v", v, v, got)
			}
		}
	})
	t.Run("ulong variants", func(t *testing.T) {
		for _, v := range []uint64{0, 42, 1_000_000_000_000} {
			e := &encoder{}
			e.writeULong(v)
			got := decodeOne(t, e.buf)
			if got != v {
				t.Fatalf("ulong(%d): expected %d, got %#v", v, v, got)
			}
		}
	})
}

func TestCodecDescribedList(t *testing.T) {
	e := &encoder{}
	e.writeDescribedList(0x10, []field{
		fieldOf(func(e *encoder) { e.writeString("container-1") }),
		omittedField(), // hostname, omitted (trailing before a set field must become null)
		fieldOf(func(e *encoder) { e.writeUint32(4096) }),
	})

	d := newDecoder(e.buf)
	v, err := d.readValue()
	if err != nil {
		t.Fatalf("readValue: %v", err)
	}
	desc, ok := v.(*described)
	if !ok {
		t.Fatalf("expected *described, got %T", v)
	}
	if desc.Descriptor != 0x10 {
		t.Fatalf("expected descriptor 0x10, got %#x", desc.Descriptor)
	}
	list, ok := desc.Value.([]any)
	if !ok {
		t.Fatalf("expected []any list value, got %T", desc.Value)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 fields (including the null placeholder), got %d: %#v", len(list), list)
	}
	if list[0] != "container-1" {
		t.Fatalf("field 0: expected %q, got %#v", "container-1", list[0])
	}
	if list[1] != nil {
		t.Fatalf("field 1 (omitted): expected nil, got %#v", list[1])
	}
	if list[2] != uint64(4096) {
		t.Fatalf("field 2: expected 4096, got %#v", list[2])
	}
}

func TestCodecDescribedListTrailingOmitShrinksList(t *testing.T) {
	e := &encoder{}
	e.writeDescribedList(0x11, []field{
		fieldOf(func(e *encoder) { e.writeUint32(1) }),
		omittedField(),
		omittedField(),
	})
	d := newDecoder(e.buf)
	v, _ := d.readValue()
	desc := v.(*described)
	list := desc.Value.([]any)
	if len(list) != 1 {
		t.Fatalf("expected trailing omits to shrink the list to 1 field, got %d: %#v", len(list), list)
	}
}

func TestCodecListEmpty(t *testing.T) {
	e := &encoder{}
	e.writeDescribedList(0x17, nil) // End has no fields at all — must encode as list0
	d := newDecoder(e.buf)
	v, err := d.readValue()
	if err != nil {
		t.Fatalf("readValue: %v", err)
	}
	desc := v.(*described)
	list, ok := desc.Value.([]any)
	if !ok || len(list) != 0 {
		t.Fatalf("expected an empty list, got %#v", desc.Value)
	}
}
