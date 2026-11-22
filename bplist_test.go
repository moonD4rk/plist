package plist

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestBplistInt128(t *testing.T) {
	bplist := []byte{0x62, 0x70, 0x6c, 0x69, 0x73, 0x74, 0x30, 0x30, 0x14, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x19}
	expected := uint64(0x090a0b0c0d0e0f10)
	buf := bytes.NewReader(bplist)
	d := newBplistParser(buf)
	pval, _ := d.parseDocument()
	if pinteger, ok := pval.(*cfInteger); !ok || pinteger.value != expected {
		t.Error("Expected", expected, "received", pval)
	}
}

func TestBplistSignedIntValues(t *testing.T) {
	bplist := []byte{
		'b', 'p', 'l', 'i', 's', 't', '0', '0',

		// Array (8 entries)
		0xA8,
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,

		// 0xFFFFFFFFFFFFFF80 (MinInt8, sign extended)
		0x13, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x80,

		// 0x7F (MaxInt8)
		0x10, 0x7f,

		// 0xFFFFFFFFFFFF8000 (MinInt16, sign extended)
		0x13, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x80, 0x00,

		// 0x7FFF (MaxInt16)
		0x11, 0x7f, 0xff,

		// 0xFFFFFFFF80000000 (MinInt32, sign extended)
		0x13, 0xff, 0xff, 0xff, 0xff, 0x80, 0x00, 0x00, 0x00,

		// 0x7FFFFFFF (MaxInt32)
		0x12, 0x7f, 0xff, 0xff, 0xff,

		// 0x8000000000000000 (MinInt64)
		0x13, 0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,

		// 0x7FFFFFFFFFFFFFFF (MaxInt64)
		0x13, 0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,

		// Offset table
		0x08, 0x11, 0x1a, 0x1c, 0x25, 0x28, 0x31, 0x36, 0x3f,

		// Trailer
		0x00, 0x00, 0x00, 0x00, 0x00,
		0x00,
		0x01,
		0x01,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x09,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x48,
	}

	expectedValues := []int64{
		math.MinInt8,
		math.MaxInt8,
		math.MinInt16,
		math.MaxInt16,
		math.MinInt32,
		math.MaxInt32,
		math.MinInt64,
		math.MaxInt64,
	}

	buf := bytes.NewReader(bplist)
	d := newBplistParser(buf)
	pval, _ := d.parseDocument()
	parsedValues := pval.(*cfArray).values
	for i, cfv := range parsedValues {
		value := int64(cfv.(*cfInteger).value)
		if value != expectedValues[i] {
			t.Error("Expected", expectedValues[i], "received", value)
		}
	}
}

func TestBplistLatin1ToUTF16(t *testing.T) {
	expectedPrefix := []byte{0x62, 0x70, 0x6c, 0x69, 0x73, 0x74, 0x30, 0x30, 0xd1, 0x01, 0x02, 0x51, 0x5f, 0x6f, 0x10, 0x80}
	expectedPostfix := []byte{0x00, 0x08, 0x00, 0x0b, 0x00, 0x0d, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x10}
	expectedBuf := bytes.NewBuffer(expectedPrefix)

	sBuf := &bytes.Buffer{}
	for i := uint16(0xc280); i <= 0xc2bf; i++ {
		binary.Write(sBuf, binary.BigEndian, i)
		binary.Write(expectedBuf, binary.BigEndian, i-0xc200)
	}

	for i := uint16(0xc380); i <= 0xc3bf; i++ {
		binary.Write(sBuf, binary.BigEndian, i)
		binary.Write(expectedBuf, binary.BigEndian, i-0xc300+0x0040)
	}

	expectedBuf.Write(expectedPostfix)

	var buf bytes.Buffer
	encoder := NewBinaryEncoder(&buf)

	data := map[string]string{
		"_": sBuf.String(),
	}
	if err := encoder.Encode(data); err != nil {
		t.Error(err.Error())
	}

	if !bytes.Equal(buf.Bytes(), expectedBuf.Bytes()) {
		t.Error("Expected", expectedBuf.Bytes(), "received", buf.Bytes())
		return
	}
}

func TestBplistNonPowerOfTwoOffsetIntSizes(t *testing.T) {
	bplist := []byte{
		'b', 'p', 'l', 'i', 's', 't', '0', '0',

		// Array (2 entries)
		0xA2,
		0x01, 0x02,

		// 0xFFFFFFFFFFFFFF80 (MinInt8, sign extended)
		0x13, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x80,

		// 0x7F (MaxInt8)
		0x10, 0x7f,

		// Offset table (each entry is 3 bytes)
		0x00, 0x00, 0x08,
		0x00, 0x00, 0x0b,
		0x00, 0x00, 0x14,

		// Trailer
		0x00, 0x00, 0x00, 0x00, 0x00,
		0x00,
		0x03,
		0x01,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x03,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x16,
	}

	buf := bytes.NewReader(bplist)
	d := newBplistParser(buf)
	_, err := d.parseDocument()
	if err != nil {
		t.Error("Unexpected error", err)
	}
}

func TestBplistDecodedValuesDoNotShareMemory(t *testing.T) {
	const text = "the string that follows the data"
	data, err := Marshal(map[string]any{"a": []byte{1, 2, 3, 4}, "b": text}, BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}

	var out struct {
		A []byte `plist:"a"`
		B string `plist:"b"`
	}
	if _, err := Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}

	grown := append(out.A, bytes.Repeat([]byte{0xFF}, len(data))...)
	if out.B != text {
		t.Errorf("appending to the decoded data changed the decoded string to %q", out.B)
	}
	if !bytes.Equal(grown[:4], []byte{1, 2, 3, 4}) {
		t.Errorf("decoded data = %v, want [1 2 3 4]", grown[:4])
	}

	if data, err = Marshal([][]byte{{1, 2, 3}, {1, 2, 3}}, BinaryFormat); err != nil {
		t.Fatal(err)
	}
	var equal [][]byte
	if _, err := Unmarshal(data, &equal); err != nil {
		t.Fatal(err)
	}
	first, second := append(equal[0], 4), append(equal[1], 5)
	if first[3] != 4 || second[3] != 5 {
		t.Errorf("appending to equal data gave %v and %v, want [1 2 3 4] and [1 2 3 5]", first, second)
	}
}

func TestBplistDataWithEqualChecksums(t *testing.T) {
	first, second := []byte("plumless"), []byte("buckeroo")
	if crc32.ChecksumIEEE(first) != crc32.ChecksumIEEE(second) {
		t.Fatal("the test needs two values with one CRC-32 checksum")
	}

	want := [][]byte{first, second, first, []byte("other"), second}
	data, err := Marshal(want, BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	var got [][]byte
	if _, err := Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decoded %q, want %q", got, want)
	}

	var root []byte
	if data, err = Marshal(second, BinaryFormat); err != nil {
		t.Fatal(err)
	}
	if _, err := Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(root, second) {
		t.Errorf("decoded %q, want %q", root, second)
	}
}

func TestBplistDatesOutsideUnixNanoRange(t *testing.T) {
	dates := []time.Time{
		{},
		time.Date(1, 1, 1, 0, 0, 0, 500000000, time.UTC),
		time.Date(1600, 2, 29, 12, 30, 15, 0, time.UTC),
		time.Date(1677, 9, 21, 0, 12, 43, 0, time.UTC),
		time.Date(2262, 4, 11, 23, 47, 17, 0, time.UTC),
		time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC),
	}
	for _, want := range dates {
		data, err := Marshal(want, BinaryFormat)
		if err != nil {
			t.Fatal(err)
		}
		var got time.Time
		if _, err := Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if !got.Equal(want) {
			t.Errorf("decoded %v, want %v", got, want)
		}
	}

	data, err := Marshal(time.Time{}, BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	if seconds := math.Float64frombits(binary.BigEndian.Uint64(data[9:])); seconds != -63113904000 {
		t.Errorf("the zero time is stored as %v seconds from 2001, want -63113904000", seconds)
	}
}

func TestBplistRealsAreUniquedByBits(t *testing.T) {
	negativeZero := math.Copysign(0, -1)
	want := []any{0.0, negativeZero, math.NaN(), float32(0), float32(negativeZero), float32(math.NaN()), math.NaN(), 0.0}

	data, err := Marshal(map[string]any{"reals": want}, BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string][]any
	if _, err := Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	got := out["reals"]
	if len(got) != len(want) {
		t.Fatalf("decoded %d reals, want %d", len(got), len(want))
	}
	for i := range want {
		if realBits(got[i]) != realBits(want[i]) {
			t.Errorf("real %d decoded as %#v, want %#v", i, got[i], want[i])
		}
	}
}

func realBits(v any) string {
	switch v := v.(type) {
	case float64:
		return fmt.Sprintf("float64 %016x", math.Float64bits(v))
	case float32:
		return fmt.Sprintf("float32 %08x", math.Float32bits(v))
	}
	return fmt.Sprintf("%T", v)
}
