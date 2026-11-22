package plist

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
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

func isUniqued(pval cfValue) bool {
	switch pval.(type) {
	case cfString, *cfInteger, *cfReal, cfDate, cfData:
		return true
	}
	return false
}

type binaryWriter struct {
	writer   *countedWriter
	indexByKey   map[any]uint64 // maps cfValue.hash() to the index of the value in objtable
	objects []cfValue
	trailer  binaryTrailer
}

func (w *binaryWriter) flatten(pval cfValue) {
	key := pval.hash()
	if isUniqued(pval) {
		if _, ok := w.indexByKey[key]; ok {
			return
		}
	}

	w.indexByKey[key] = uint64(len(w.objects))
	w.objects = append(w.objects, pval)

	switch pval := pval.(type) {
	case *cfDict:
		pval.sort()
		for _, k := range pval.keys {
			w.flatten(cfString(k))
		}
		for _, v := range pval.values {
			w.flatten(v)
		}
	case *cfArray:
		for _, v := range pval.values {
			if v != nil {
				w.flatten(v)
			}
		}
	}
}

func (w *binaryWriter) indexOf(pval cfValue) (uint64, bool) {
	v, ok := w.indexByKey[pval.hash()]
	return v, ok
}

func (w *binaryWriter) generateDocument(root cfValue) {
	w.objects = make([]cfValue, 0, 16)
	w.indexByKey = make(map[any]uint64)
	w.flatten(root)

	w.trailer.NumObjects = uint64(len(w.objects))
	w.trailer.ObjectRefSize = uint8(minIntSize(w.trailer.NumObjects))

	w.writer.Write([]byte("bplist00"))

	offsets := make([]uint64, w.trailer.NumObjects)
	for i, pval := range w.objects {
		offsets[i] = uint64(w.writer.BytesWritten())
		w.writeObject(pval)
	}

	w.trailer.OffsetIntSize = uint8(minIntSize(uint64(w.writer.BytesWritten())))
	w.trailer.TopObject = w.indexByKey[root.hash()]
	w.trailer.OffsetTableOffset = uint64(w.writer.BytesWritten())

	for _, offset := range offsets {
		w.writeSizedInt(offset, int(w.trailer.OffsetIntSize))
	}

	binary.Write(w.writer, binary.BigEndian, w.trailer)
}

func (w *binaryWriter) writeObject(pval cfValue) {
	if pval == nil {
		return
	}

	switch pval := pval.(type) {
	case *cfDict:
		w.writeDict(pval)
	case *cfArray:
		w.writeArray(pval.values)
	case cfString:
		w.writeString(string(pval))
	case *cfInteger:
		w.writeInt(pval.signed, pval.value)
	case *cfReal:
		if pval.is32 {
			w.writeReal(pval.value, 32)
		} else {
			w.writeReal(pval.value, 64)
		}
	case cfBool:
		w.writeBool(bool(pval))
	case cfData:
		w.writeData([]byte(pval))
	case cfDate:
		w.writeDate(time.Time(pval))
	case cfUID:
		w.writeUID(UID(pval))
	default:
		panic(fmt.Errorf("unknown plist type %T", pval))
	}
}

func (w *binaryWriter) writeSizedInt(n uint64, nbytes int) {
	var val any
	switch nbytes {
	case 1:
		val = uint8(n)
	case 2:
		val = uint16(n)
	case 4:
		val = uint32(n)
	case 8:
		val = n
	default:
		panic(errors.New("illegal integer size"))
	}
	binary.Write(w.writer, binary.BigEndian, val)
}

func (w *binaryWriter) writeBool(v bool) {
	tag := uint8(markerFalse)
	if v {
		tag = markerTrue
	}
	binary.Write(w.writer, binary.BigEndian, tag)
}

func (w *binaryWriter) writeInt(signed bool, n uint64) {
	var tag uint8
	var val any
	switch {
	case n <= uint64(0xff):
		val = uint8(n)
		tag = markerInt | 0x0
	case n <= uint64(0xffff):
		val = uint16(n)
		tag = markerInt | 0x1
	case n <= uint64(0xffffffff):
		val = uint32(n)
		tag = markerInt | 0x2
	case n > uint64(0x7fffffffffffffff) && !signed:
		// 64-bit values are always *signed* in format 00.
		// Any unsigned value that doesn't intersect with the signed
		// range must be sign-extended and stored as a SInt128
		val = n
		tag = markerInt | 0x4
	default:
		val = n
		tag = markerInt | 0x3
	}

	binary.Write(w.writer, binary.BigEndian, tag)
	if tag&0xF == 0x4 {
		// SInt128; in the absence of true 128-bit integers in Go,
		// we'll just fake the top half. We only got here because
		// we had an unsigned 64-bit int that didn't fit,
		// so sign extend it with zeroes.
		binary.Write(w.writer, binary.BigEndian, uint64(0))
	}
	binary.Write(w.writer, binary.BigEndian, val)
}

func (w *binaryWriter) writeUID(u UID) {
	nbytes := minIntSize(uint64(u))
	tag := uint8(markerUID | (nbytes - 1))

	binary.Write(w.writer, binary.BigEndian, tag)
	w.writeSizedInt(uint64(u), nbytes)
}

func (w *binaryWriter) writeReal(n float64, bits int) {
	var tag uint8 = markerReal | 0x3
	var val any = n
	if bits == 32 {
		val = float32(n)
		tag = markerReal | 0x2
	}

	binary.Write(w.writer, binary.BigEndian, tag)
	binary.Write(w.writer, binary.BigEndian, val)
}

func (w *binaryWriter) writeDate(t time.Time) {
	tag := uint8(markerDate) | 0x3
	val := float64(t.Unix()) + float64(t.Nanosecond())/float64(time.Second)
	// UnixNano overflows outside the years 1678 to 2262; inside them it keeps the output of earlier releases.
	if nano := t.UnixNano(); time.Unix(0, nano).Equal(t) {
		val = float64(nano) / float64(time.Second)
	}
	val -= 978307200 // Adjust to Apple Epoch

	binary.Write(w.writer, binary.BigEndian, tag)
	binary.Write(w.writer, binary.BigEndian, val)
}

func (w *binaryWriter) writeMarker(tag uint8, count uint64) {
	marker := tag
	if count >= 0xF {
		marker |= 0xF
	} else {
		marker |= uint8(count)
	}

	binary.Write(w.writer, binary.BigEndian, marker)

	if count >= 0xF {
		w.writeInt(false, count)
	}
}

func (w *binaryWriter) writeData(data []byte) {
	w.writeMarker(markerData, uint64(len(data)))
	binary.Write(w.writer, binary.BigEndian, data)
}

func (w *binaryWriter) writeString(str string) {
	for _, r := range str {
		if r > 0x7F {
			utf16Runes := utf16.Encode([]rune(str))
			w.writeMarker(markerUTF16, uint64(len(utf16Runes)))
			binary.Write(w.writer, binary.BigEndian, utf16Runes)
			return
		}
	}

	w.writeMarker(markerASCII, uint64(len(str)))
	binary.Write(w.writer, binary.BigEndian, []byte(str))
}

func (w *binaryWriter) writeDict(dict *cfDict) {
	// assumption: sorted already; flattenPlistValue did this.
	cnt := len(dict.keys)
	w.writeMarker(markerDict, uint64(cnt))
	vals := make([]uint64, cnt*2)
	for i, k := range dict.keys {
		// invariant: keys have already been "uniqued" (as PStrings)
		keyIdx, ok := w.indexByKey[cfString(k).hash()]
		if !ok {
			panic(errors.New("failed to find key " + k + " in object map during serialization"))
		}
		vals[i] = keyIdx
	}

	for i, v := range dict.values {
		// invariant: values have already been "uniqued"
		objIdx, ok := w.indexOf(v)
		if !ok {
			panic(errors.New("failed to find value in object map during serialization"))
		}
		vals[i+cnt] = objIdx
	}

	for _, v := range vals {
		w.writeSizedInt(v, int(w.trailer.ObjectRefSize))
	}
}

func (w *binaryWriter) writeArray(arr []cfValue) {
	count := 0
	for _, v := range arr {
		if v != nil {
			count++
		}
	}

	w.writeMarker(markerArray, uint64(count))
	for _, v := range arr {
		if v == nil {
			continue
		}
		objIdx, ok := w.indexOf(v)
		if !ok {
			panic(errors.New("failed to find value in object map during serialization"))
		}

		w.writeSizedInt(objIdx, int(w.trailer.ObjectRefSize))
	}
}

func (w *binaryWriter) Indent(i string) {
	// There's nothing to indent.
}

func newBinaryWriter(w io.Writer) *binaryWriter {
	return &binaryWriter{
		writer: &countedWriter{Writer: mustWriter{w}},
	}
}
