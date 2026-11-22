package plist

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
	"unicode/utf16"
)

func minIntSize(n uint64) int {
	switch {
	case n <= uint64(0xff):
		return 1
	case n <= uint64(0xffff):
		return 2
	case n <= uint64(0xffffffff):
		return 4
	default:
		return 8
	}
}

func isUniqued(value cfValue) bool {
	switch value.(type) {
	case cfString, *cfInteger, *cfReal, cfDate, cfData:
		return true
	}
	return false
}

// realKey compares bit patterns: compared as numbers, NaN never equals itself and -0 equals +0.
type realKey struct {
	is32 bool
	bits uint64
}

func realKeyOf(r *cfReal) realKey {
	if r.is32 {
		return realKey{is32: true, bits: uint64(math.Float32bits(float32(r.value)))}
	}
	return realKey{bits: math.Float64bits(r.value)}
}

type binaryWriter struct {
	out     io.Writer
	written uint64

	objects []cfValue
	refSize int

	// Object IDs by value. The first five kinds are stored once per distinct value.
	strings  map[string]uint64
	data     map[string]uint64
	integers map[cfInteger]uint64
	reals    map[realKey]uint64
	dates    map[time.Time]uint64
	// Containers by identity; booleans and UIDs by value, stored per occurrence with the last one winning.
	others map[cfValue]uint64
}

func (w *binaryWriter) indexOf(value cfValue) (index uint64, ok bool) {
	switch value := value.(type) {
	case cfString:
		index, ok = w.strings[string(value)]
	case cfData:
		index, ok = w.data[string(value)]
	case *cfInteger:
		index, ok = w.integers[*value]
	case *cfReal:
		index, ok = w.reals[realKeyOf(value)]
	case cfDate:
		index, ok = w.dates[time.Time(value)]
	default:
		index, ok = w.others[value]
	}
	return index, ok
}

func (w *binaryWriter) add(value cfValue) {
	index := uint64(len(w.objects))
	w.objects = append(w.objects, value)

	switch value := value.(type) {
	case cfString:
		w.strings[string(value)] = index
	case cfData:
		// The key views the bytes instead of copying them; they do not change while the document is written.
		w.data[zeroCopy8BitString(value, 0, len(value))] = index
	case *cfInteger:
		w.integers[*value] = index
	case *cfReal:
		w.reals[realKeyOf(value)] = index
	case cfDate:
		w.dates[time.Time(value)] = index
	default:
		w.others[value] = index
	}
}

func (w *binaryWriter) write(b []byte) {
	n, err := w.out.Write(b)
	w.written += uint64(n)
	if err != nil {
		panic(err)
	}
}

// Every write gets a slice of its own: v1 did the same, and a caller's Writer may keep what it is given.
func (w *binaryWriter) writeByte(b byte) {
	w.write([]byte{b})
}

func (w *binaryWriter) flatten(value cfValue) {
	if isUniqued(value) {
		if _, ok := w.indexOf(value); ok {
			return
		}
	}
	w.add(value)

	switch value := value.(type) {
	case *cfDict:
		value.sort()
		for _, k := range value.keys {
			w.flatten(cfString(k))
		}
		for _, v := range value.values {
			w.flatten(v)
		}
	case *cfArray:
		for _, v := range value.values {
			if v != nil {
				w.flatten(v)
			}
		}
	}
}

func (w *binaryWriter) generateDocument(root cfValue) {
	w.objects = make([]cfValue, 0, 16)
	w.strings = make(map[string]uint64)
	w.data = make(map[string]uint64)
	w.integers = make(map[cfInteger]uint64)
	w.reals = make(map[realKey]uint64)
	w.dates = make(map[time.Time]uint64)
	w.others = make(map[cfValue]uint64)
	w.flatten(root)

	trailer := binaryTrailer{objectCount: uint64(len(w.objects))}
	w.refSize = minIntSize(trailer.objectCount)
	trailer.refSize = uint8(w.refSize)

	w.write([]byte("bplist00"))

	offsets := make([]uint64, len(w.objects))
	for i, object := range w.objects {
		offsets[i] = w.written
		w.writeObject(object)
	}

	trailer.offsetSize = uint8(minIntSize(w.written))
	trailer.rootIndex, _ = w.indexOf(root)
	trailer.offsetTableStart = w.written

	for _, offset := range offsets {
		w.writeSizedInt(offset, int(trailer.offsetSize))
	}
	w.writeTrailer(trailer)
}

func (w *binaryWriter) writeTrailer(trailer binaryTrailer) {
	var b [binaryTrailerSize]byte
	b[6] = trailer.offsetSize
	b[7] = trailer.refSize
	binary.BigEndian.PutUint64(b[8:], trailer.objectCount)
	binary.BigEndian.PutUint64(b[16:], trailer.rootIndex)
	binary.BigEndian.PutUint64(b[24:], trailer.offsetTableStart)
	w.write(b[:])
}

func (w *binaryWriter) writeObject(value cfValue) {
	if value == nil {
		return
	}

	switch value := value.(type) {
	case *cfDict:
		w.writeDict(value)
	case *cfArray:
		w.writeArray(value.values)
	case cfString:
		w.writeString(string(value))
	case *cfInteger:
		w.writeInt(value.signed, value.value)
	case *cfReal:
		w.writeReal(value)
	case cfBool:
		w.writeBool(bool(value))
	case cfData:
		w.writeData([]byte(value))
	case cfDate:
		w.writeDate(time.Time(value))
	case cfUID:
		w.writeUID(UID(value))
	default:
		panic(fmt.Errorf("unknown plist type %T", value))
	}
}

func (w *binaryWriter) writeSizedInt(n uint64, nbytes int) {
	switch nbytes {
	case 1, 2, 4, 8:
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], n)
		w.write(b[8-nbytes:])
	default:
		panic(errors.New("illegal integer size"))
	}
}

func (w *binaryWriter) writeBool(v bool) {
	if v {
		w.writeByte(markerTrue)
	} else {
		w.writeByte(markerFalse)
	}
}

func (w *binaryWriter) writeInt(signed bool, n uint64) {
	switch {
	case n <= uint64(0xff):
		w.writeByte(markerInt | 0x0)
		w.writeSizedInt(n, 1)
	case n <= uint64(0xffff):
		w.writeByte(markerInt | 0x1)
		w.writeSizedInt(n, 2)
	case n <= uint64(0xffffffff):
		w.writeByte(markerInt | 0x2)
		w.writeSizedInt(n, 4)
	case n > uint64(0x7fffffffffffffff) && !signed:
		// Eight bytes are read as a signed integer, so a larger unsigned one takes sixteen with the high half zero.
		w.writeByte(markerInt | 0x4)
		w.writeSizedInt(0, 8)
		w.writeSizedInt(n, 8)
	default:
		w.writeByte(markerInt | 0x3)
		w.writeSizedInt(n, 8)
	}
}

func (w *binaryWriter) writeUID(u UID) {
	nbytes := minIntSize(uint64(u))
	w.writeByte(uint8(markerUID | (nbytes - 1)))
	w.writeSizedInt(uint64(u), nbytes)
}

func (w *binaryWriter) writeReal(r *cfReal) {
	if r.is32 {
		w.writeByte(markerReal | 0x2)
		w.writeSizedInt(uint64(math.Float32bits(float32(r.value))), 4)
		return
	}
	w.writeByte(markerReal | 0x3)
	w.writeSizedInt(math.Float64bits(r.value), 8)
}

func (w *binaryWriter) writeDate(t time.Time) {
	seconds := float64(t.Unix()) + float64(t.Nanosecond())/float64(time.Second)
	// UnixNano overflows outside the years 1678 to 2262; inside them it keeps the output of earlier releases.
	if nano := t.UnixNano(); time.Unix(0, nano).Equal(t) {
		seconds = float64(nano) / float64(time.Second)
	}
	seconds -= appleEpochOffset

	w.writeByte(markerDate | 0x3)
	w.writeSizedInt(math.Float64bits(seconds), 8)
}

func (w *binaryWriter) writeMarker(marker uint8, count uint64) {
	if count < 0xF {
		w.writeByte(marker | uint8(count))
		return
	}
	w.writeByte(marker | 0xF)
	w.writeInt(false, count)
}

func (w *binaryWriter) writeData(data []byte) {
	w.writeMarker(markerData, uint64(len(data)))
	w.write(data)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7F {
			return false
		}
	}
	return true
}

func (w *binaryWriter) writeString(str string) {
	if isASCII(str) {
		w.writeMarker(markerASCII, uint64(len(str)))
		w.write([]byte(str))
		return
	}

	units := utf16.Encode([]rune(str))
	w.writeMarker(markerUTF16, uint64(len(units)))

	b := make([]byte, 2*len(units))
	for i, unit := range units {
		binary.BigEndian.PutUint16(b[2*i:], unit)
	}
	w.write(b)
}

func (w *binaryWriter) writeDict(dict *cfDict) {
	// assumption: sorted already; flatten did this.
	count := len(dict.keys)
	w.writeMarker(markerDict, uint64(count))
	refs := make([]uint64, count*2)
	for i, k := range dict.keys {
		index, ok := w.indexOf(cfString(k))
		if !ok {
			panic(errors.New("failed to find key " + k + " in object map during serialization"))
		}
		refs[i] = index
	}

	for i, v := range dict.values {
		index, ok := w.indexOf(v)
		if !ok {
			panic(errors.New("failed to find value in object map during serialization"))
		}
		refs[i+count] = index
	}

	for _, ref := range refs {
		w.writeSizedInt(ref, w.refSize)
	}
}

func (w *binaryWriter) writeArray(values []cfValue) {
	count := 0
	for _, v := range values {
		if v != nil {
			count++
		}
	}

	w.writeMarker(markerArray, uint64(count))
	for _, v := range values {
		if v == nil {
			continue
		}
		index, ok := w.indexOf(v)
		if !ok {
			panic(errors.New("failed to find value in object map during serialization"))
		}

		w.writeSizedInt(index, w.refSize)
	}
}

func (w *binaryWriter) Indent(i string) {
	// There's nothing to indent.
}

func newBinaryWriter(w io.Writer) *binaryWriter {
	return &binaryWriter{out: w}
}
