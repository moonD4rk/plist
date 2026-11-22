package plist

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"time"
	"unicode/utf16"
)

type binaryTrailer struct {
	Unused            [5]uint8
	SortVersion       uint8
	OffsetIntSize     uint8
	ObjectRefSize     uint8
	NumObjects        uint64
	TopObject         uint64
	OffsetTableOffset uint64
}

const (
	markerNull        uint8 = 0x00
	markerFalse         = 0x08
	markerTrue          = 0x09
	markerInt           = 0x10
	markerReal              = 0x20
	markerDate              = 0x30
	markerData              = 0x40
	markerASCII       = 0x50
	markerUTF16       = 0x60
	markerUID               = 0x80
	markerArray             = 0xA0
	markerDict        = 0xD0
)

const (
	signedHighBits = 0xFFFFFFFFFFFFFFFF
)

type offset uint64

// valueShape measures an object with every shared reference followed, which is how it gets unmarshaled.
type valueShape struct {
	values uint64 // values in the tree, the object itself included
	depth  int    // levels of nested containers
}

type binaryParser struct {
	buffer []byte

	reader        io.ReadSeeker
	version       int
	objects       []cfValue    // object ID to object
	shapes        []valueShape // object ID to the shape of its object
	trailer       binaryTrailer
	trailerStart uint64

	enclosing []offset // slice of object offsets; manipulated during container deserialization

	budget uint64 // see valueBudget
	work   uint64 // object references and bytes of strings and data read so far
}

func (p *binaryParser) validateTrailer() {
	if p.trailer.OffsetTableOffset >= p.trailerStart {
		panic(fmt.Errorf("offset table beyond beginning of trailer (0x%x, trailer@0x%x)", p.trailer.OffsetTableOffset, p.trailerStart))
	}

	if p.trailer.OffsetTableOffset < 9 {
		panic(fmt.Errorf("offset table begins inside header (0x%x)", p.trailer.OffsetTableOffset))
	}

	if p.trailerStart > (p.trailer.NumObjects*uint64(p.trailer.OffsetIntSize))+p.trailer.OffsetTableOffset {
		panic(errors.New("garbage between offset table and trailer"))
	}

	if p.trailer.OffsetTableOffset+(uint64(p.trailer.OffsetIntSize)*p.trailer.NumObjects) > p.trailerStart {
		panic(errors.New("offset table isn't long enough to address every object"))
	}

	maxObjectRef := uint64(1) << (8 * p.trailer.ObjectRefSize)
	if p.trailer.NumObjects > maxObjectRef {
		panic(fmt.Errorf("more objects (%v) than object ref size (%v bytes) can support", p.trailer.NumObjects, p.trailer.ObjectRefSize))
	}

	if p.trailer.OffsetIntSize < uint8(8) && (uint64(1)<<(8*p.trailer.OffsetIntSize)) <= p.trailer.OffsetTableOffset {
		panic(errors.New("offset size isn't big enough to address entire file"))
	}

	if p.trailer.TopObject >= p.trailer.NumObjects {
		panic(fmt.Errorf("top object #%d is out of range (only %d exist)", p.trailer.TopObject, p.trailer.NumObjects))
	}
}

func (p *binaryParser) parseDocument() (pval cfValue, parseError error) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(runtime.Error); ok {
				panic(r)
			}

			err, ok := r.(error)
			if !ok {
				panic(r)
			}

			parseError = syntaxError{"binary", err}
		}
	}()

	buf, err := io.ReadAll(p.reader)
	if err != nil {
		panic(err)
	}
	p.buffer = buf

	l := len(p.buffer)
	if l < 40 {
		panic(errors.New("not enough data"))
	}

	if !bytes.Equal(p.buffer[0:6], []byte{'b', 'p', 'l', 'i', 's', 't'}) {
		panic(errors.New("incomprehensible magic"))
	}

	p.version = int(((p.buffer[6] - '0') * 10) + (p.buffer[7] - '0'))

	if p.version > 1 {
		panic(fmt.Errorf("unexpected version %d", p.version))
	}

	p.trailerStart = uint64(l - 32)
	p.trailer = binaryTrailer{
		SortVersion:       p.buffer[p.trailerStart+5],
		OffsetIntSize:     p.buffer[p.trailerStart+6],
		ObjectRefSize:     p.buffer[p.trailerStart+7],
		NumObjects:        binary.BigEndian.Uint64(p.buffer[p.trailerStart+8:]),
		TopObject:         binary.BigEndian.Uint64(p.buffer[p.trailerStart+16:]),
		OffsetTableOffset: binary.BigEndian.Uint64(p.buffer[p.trailerStart+24:]),
	}

	p.validateTrailer()

	// INVARIANTS:
	// - Entire offset table is before trailer
	// - Offset table begins after header
	// - Offset table can address entire document
	// - Object IDs are big enough to support the number of objects in this plist
	// - Top object is in range

	p.objects = make([]cfValue, p.trailer.NumObjects)
	p.shapes = make([]valueShape, p.trailer.NumObjects)
	p.budget = valueBudget(len(p.buffer))

	pval = p.object(p.trailer.TopObject)
	return pval, parseError
}

// readSizedInt returns a 128-bit integer as low64, high64
func (p *binaryParser) readSizedInt(off offset, nbytes int) (lo, hi uint64, newOffset offset) {
	// Per comments in CoreFoundation, format version 00 requires that all
	// 1, 2 or 4-byte integers be interpreted as unsigned. 8-byte integers are
	// signed (always?) and therefore must be sign extended here.
	// negative 1, 2, or 4-byte integers are always emitted as 64-bit.
	switch nbytes {
	case 1:
		p.checkRange(off, 1) // runzero patch
		lo, hi = uint64(p.buffer[off]), 0
	case 2:
		p.checkRange(off, 2) // runzero patch
		lo, hi = uint64(binary.BigEndian.Uint16(p.buffer[off:])), 0
	case 4:
		p.checkRange(off, 4) // runzero patch
		lo, hi = uint64(binary.BigEndian.Uint32(p.buffer[off:])), 0
	case 8:
		p.checkRange(off, 8) // runzero patch
		lo = binary.BigEndian.Uint64(p.buffer[off:])
		if p.buffer[off]&0x80 != 0 {
			// sign extend if lo is signed
			hi = signedHighBits
		}
	case 16:
		p.checkRange(off, 16) // runzero patch
		lo, hi = binary.BigEndian.Uint64(p.buffer[off+8:]), binary.BigEndian.Uint64(p.buffer[off:])
	default:
		if nbytes > 8 {
			panic(errors.New("illegal integer size"))
		}
		// runzero patch: the read is an 8-byte big-endian load
		// ending at off+nbytes; its start is off-(8-nbytes). Reject if it would
		// underflow before zero or extend past the buffer.
		if offset(8-nbytes) > off {
			panic(fmt.Errorf("integer read at offset 0x%x underflows buffer", off))
		}
		p.checkRange(off-(8-offset(nbytes)), 8)
		lo, hi = binary.BigEndian.Uint64(p.buffer[off-(8-offset(nbytes)):])&((1<<offset(nbytes*8))-1), 0
	}
	newOffset = off + offset(nbytes)
	return lo, hi, newOffset
}

func (p *binaryParser) readRef(off offset) (uint64, offset) {
	oid, _, next := p.readSizedInt(off, int(p.trailer.ObjectRefSize))
	return oid, next
}

func (p *binaryParser) readOffset(off offset) (offset, offset) {
	parsedOffset, _, next := p.readSizedInt(off, int(p.trailer.OffsetIntSize))
	return offset(parsedOffset), next
}

func (p *binaryParser) object(index uint64) cfValue {
	if index >= p.trailer.NumObjects {
		panic(fmt.Errorf("invalid object#%d (max %d)", index, p.trailer.NumObjects))
	}

	if pval := p.objects[index]; pval != nil {
		return pval
	}

	off, _ := p.readOffset(offset(p.trailer.OffsetTableOffset + (index * uint64(p.trailer.OffsetIntSize))))
	if off > offset(p.trailer.OffsetTableOffset-1) {
		panic(fmt.Errorf("object#%d starts beyond beginning of object table (0x%x, table@0x%x)", index, off, p.trailer.OffsetTableOffset))
	}

	pval, shape := p.readObject(off)
	p.objects[index] = pval
	p.shapes[index] = shape
	return pval
}

// checkRange panics with a (recoverable, non-runtime) parse error if the
// half-open byte range [start, start+length) is not fully contained within the
// parsed buffer. runzero patch: every offset/length read derived
// from attacker-controlled offset-table or count fields must be bounds-checked
// before slicing so a crafted bplist cannot trigger an out-of-bounds slice
// panic (which the package deliberately re-throws past Decode as a runtime
// error). Using uint64 math throughout avoids wraparound on near-overflow
// values.
func (p *binaryParser) checkRange(start offset, length uint64) {
	bufLen := uint64(len(p.buffer))
	s := uint64(start)
	if s > bufLen || length > bufLen-s {
		panic(fmt.Errorf("read of %d bytes at offset 0x%x exceeds buffer length %d", length, s, bufLen))
	}
}

// Objects that do not overlap hold fewer references and payload bytes than the document has bytes.
func (p *binaryParser) charge(n uint64) {
	p.work += n
	if p.work > p.budget {
		panic(errTooManyValues)
	}
}

func (p *binaryParser) enter(off offset) {
	// runzero patch: bound container-nesting depth. The existing
	// loop only rejects an exact self-reference already on the stack; a chain
	// of N distinct nested containers would otherwise recurse N deep and can
	// overflow the goroutine stack (an unrecoverable fatal error) on a small
	// crafted binary plist. containerStack length is the current depth.
	if len(p.enclosing) >= maxParseDepth {
		panic(errMaxDepthExceeded)
	}
	for _, v := range p.enclosing {
		if v == off {
			p.failSelfReference(off)
		}
	}
	p.enclosing = append(p.enclosing, off)
}

func (p *binaryParser) failSelfReference(off offset) {
	ids := ""
	for _, v := range p.enclosing {
		ids += fmt.Sprintf("0x%x > ", v)
	}

	// %s0x%x: ids above ends with " > "
	panic(fmt.Errorf("self-referential collection@0x%x (%s0x%x) cannot be deserialized", off, ids, off))
}

func (p *binaryParser) leave() {
	p.enclosing = p.enclosing[:len(p.enclosing)-1]
}

func (p *binaryParser) readObject(off offset) (cfValue, valueShape) {
	p.checkRange(off, 1) // runzero patch

	switch p.buffer[off] & 0xF0 {
	case markerDict:
		return p.readDict(off)
	case markerArray:
		return p.readArray(off)
	}
	return p.readScalar(off), valueShape{values: 1}
}

func (p *binaryParser) readScalar(off offset) cfValue {
	tag := p.buffer[off]

	switch tag & 0xF0 {
	case markerNull:
		switch tag & 0x0F {
		case markerTrue, markerFalse:
			return cfBool(tag == markerTrue)
		}
	case markerInt:
		lo, hi, _ := p.readInt(off)
		return &cfInteger{
			signed: hi == signedHighBits, // a signed integer is stored as a 128-bit integer with the top 64 bits set
			value:  lo,
		}
	case markerReal:
		nbytes := 1 << (tag & 0x0F)
		switch nbytes {
		case 4:
			p.checkRange(off+1, 4) // runzero patch
			bits := binary.BigEndian.Uint32(p.buffer[off+1:])
			return &cfReal{is32: true, value: float64(math.Float32frombits(bits))}
		case 8:
			p.checkRange(off+1, 8) // runzero patch
			bits := binary.BigEndian.Uint64(p.buffer[off+1:])
			return &cfReal{value: math.Float64frombits(bits)}
		}
		panic(errors.New("illegal float size"))
	case markerDate:
		p.checkRange(off+1, 8) // runzero patch
		bits := binary.BigEndian.Uint64(p.buffer[off+1:])
		val := math.Float64frombits(bits)

		// Apple Epoch is 20010101000000Z
		// Adjust for UNIX Time
		val += 978307200

		sec, fsec := math.Modf(val)
		tm := time.Unix(int64(sec), int64(fsec*float64(time.Second))).In(time.UTC)
		return cfDate(tm)
	case markerData:
		data := p.readData(off)
		return cfData(data)
	case markerASCII:
		str := p.readASCII(off)
		return cfString(str)
	case markerUTF16:
		str := p.readUTF16(off)
		return cfString(str)
	case markerUID: // Somehow different than int: low half is nbytes - 1 instead of log2(nbytes)
		lo, _, _ := p.readSizedInt(off+1, int(tag&0xF)+1)
		return cfUID(lo)
	}
	panic(fmt.Errorf("unexpected atom 0x%2.02x at offset 0x%x", tag, off))
}

func (p *binaryParser) readInt(off offset) (uint64, uint64, offset) {
	tag := p.buffer[off]
	return p.readSizedInt(off+1, 1<<(tag&0xF))
}

func (p *binaryParser) readCount(off offset) (uint64, offset) {
	tag := p.buffer[off]
	cnt := uint64(tag & 0x0F)
	if cnt == 0xF {
		cnt, _, off = p.readInt(off + 1)
		// runzero patch: an extended count is an attacker-controlled 8-byte
		// integer. A real element/byte count can never exceed the object region
		// (which ends at OffsetTableOffset), so reject larger values HERE, before
		// any downstream count*K / start+count arithmetic can wrap uint64 and
		// slip a huge length past the per-consumer bounds checks into
		// make()/slice/unsafe.String — an unrecoverable runtime panic that
		// escapes Decode's recover and crashes the caller.
		if cnt > p.trailer.OffsetTableOffset {
			panic(fmt.Errorf("element count %d at offset 0x%x exceeds object region (table@0x%x)", cnt, off, p.trailer.OffsetTableOffset))
		}
		return cnt, off
	}
	return cnt, off + 1
}

func (p *binaryParser) readData(off offset) []byte {
	count, start := p.readCount(off)
	if start+offset(count) > offset(p.trailer.OffsetTableOffset) {
		panic(fmt.Errorf("data@0x%x too long (%v bytes, max is %v)", off, count, p.trailer.OffsetTableOffset-uint64(start)))
	}
	p.charge(count)
	// The copy has no spare capacity, so appending to it cannot write into memory another reference sees.
	data := make([]byte, count)
	copy(data, p.buffer[start:])
	return data
}

func (p *binaryParser) readASCII(off offset) string {
	count, start := p.readCount(off)
	if start+offset(count) > offset(p.trailer.OffsetTableOffset) {
		panic(fmt.Errorf("ascii string@0x%x too long (%v bytes, max is %v)", off, count, p.trailer.OffsetTableOffset-uint64(start)))
	}

	p.charge(count)
	return string(p.buffer[start : start+offset(count)])
}

func (p *binaryParser) readUTF16(off offset) string {
	count, start := p.readCount(off)
	byteLen := count * 2
	if start+offset(byteLen) > offset(p.trailer.OffsetTableOffset) {
		panic(fmt.Errorf("utf16 string@0x%x too long (%v bytes, max is %v)", off, byteLen, p.trailer.OffsetTableOffset-uint64(start)))
	}

	p.charge(byteLen)
	u16s := make([]uint16, count)
	for i := offset(0); i < offset(count); i++ {
		u16s[i] = binary.BigEndian.Uint16(p.buffer[start+(i*2):])
	}
	runes := utf16.Decode(u16s)
	return string(runes)
}

// Charging references as they are read bounds parsing; limiting the shape bounds unmarshaling.
func (p *binaryParser) readObjectList(off offset, count uint64) ([]cfValue, valueShape) {
	if off+offset(count*uint64(p.trailer.ObjectRefSize)) > offset(p.trailer.OffsetTableOffset) {
		panic(fmt.Errorf("list@0x%x length (%v) puts its end beyond the offset table at 0x%x", off, count, p.trailer.OffsetTableOffset))
	}
	p.charge(count)
	objects := make([]cfValue, count)
	shape := valueShape{values: 1}

	next := off
	var oid uint64
	for i := uint64(0); i < count; i++ {
		oid, next = p.readRef(next)
		objects[i] = p.object(oid)

		child := p.shapes[oid]
		shape.values += child.values
		if shape.values > p.budget {
			panic(errTooManyValues)
		}
		if child.depth > shape.depth {
			shape.depth = child.depth
		}
	}

	shape.depth++
	if shape.depth > maxParseDepth {
		panic(errMaxDepthExceeded)
	}
	return objects, shape
}

func (p *binaryParser) readDict(off offset) (*cfDict, valueShape) {
	p.enter(off)
	defer p.leave()

	// A dictionary is an object list of [keys... values...].
	cnt, start := p.readCount(off)
	objects, shape := p.readObjectList(start, cnt*2)

	keys := make([]string, cnt)
	for i := uint64(0); i < cnt; i++ {
		if str, ok := objects[i].(cfString); ok {
			keys[i] = string(str)
		} else {
			panic(fmt.Errorf("dictionary@0x%x contains non-string key at index %d", off, i))
		}
	}

	return &cfDict{
		keys:   keys,
		values: objects[cnt:],
	}, shape
}

func (p *binaryParser) readArray(off offset) (*cfArray, valueShape) {
	p.enter(off)
	defer p.leave()

	// an array is just an object list
	cnt, start := p.readCount(off)
	objects, shape := p.readObjectList(start, cnt)
	return &cfArray{objects}, shape
}

func newBinaryParser(r io.ReadSeeker) *binaryParser {
	return &binaryParser{reader: r}
}
