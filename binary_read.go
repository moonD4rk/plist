package plist

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"
)

// The high four bits of the byte an object starts with say what the object is.
const (
	markerNull  = 0x00
	markerFalse = 0x08
	markerTrue  = 0x09
	markerInt   = 0x10
	markerReal  = 0x20
	markerDate  = 0x30
	markerData  = 0x40
	markerASCII = 0x50
	markerUTF16 = 0x60
	markerUID   = 0x80
	markerArray = 0xA0
	markerDict  = 0xD0
)

const (
	binaryHeaderSize  = 8
	binaryTrailerSize = 32

	// Dates count seconds from 2001-01-01T00:00:00Z.
	appleEpochOffset = 978307200

	signedHighBits = 0xFFFFFFFFFFFFFFFF
)

var binaryMagic = []byte("bplist")

type binaryTrailer struct {
	offsetSize       uint8
	refSize          uint8
	objectCount      uint64
	rootIndex        uint64
	offsetTableStart uint64
}

// valueShape measures an object with every shared reference followed, which is how it gets unmarshaled.
type valueShape struct {
	values uint64 // values in the tree, the object itself included
	depth  int    // levels of nested containers
}

type binaryParser struct {
	reader io.Reader
	buffer []byte

	trailer      binaryTrailer
	trailerStart uint64

	objects   []cfValue    // by object ID
	shapes    []valueShape // by object ID
	enclosing []uint64     // offsets of the containers being read

	budget uint64 // see valueBudget
	work   uint64 // object references and bytes of strings and data read so far
}

func (p *binaryParser) validateTrailer() {
	if p.trailer.offsetTableStart >= p.trailerStart {
		panic(fmt.Errorf("offset table beyond beginning of trailer (0x%x, trailer@0x%x)", p.trailer.offsetTableStart, p.trailerStart))
	}

	if p.trailer.offsetTableStart < 9 {
		panic(fmt.Errorf("offset table begins inside header (0x%x)", p.trailer.offsetTableStart))
	}

	if p.trailerStart > (p.trailer.objectCount*uint64(p.trailer.offsetSize))+p.trailer.offsetTableStart {
		panic(errors.New("garbage between offset table and trailer"))
	}

	if p.trailer.offsetTableStart+(uint64(p.trailer.offsetSize)*p.trailer.objectCount) > p.trailerStart {
		panic(errors.New("offset table isn't long enough to address every object"))
	}

	maxObjectRef := uint64(1) << (8 * p.trailer.refSize)
	if p.trailer.objectCount > maxObjectRef {
		panic(fmt.Errorf("more objects (%v) than object ref size (%v bytes) can support", p.trailer.objectCount, p.trailer.refSize))
	}

	if p.trailer.offsetSize < uint8(8) && (uint64(1)<<(8*p.trailer.offsetSize)) <= p.trailer.offsetTableStart {
		panic(errors.New("offset size isn't big enough to address entire file"))
	}

	if p.trailer.rootIndex >= p.trailer.objectCount {
		panic(fmt.Errorf("top object #%d is out of range (only %d exist)", p.trailer.rootIndex, p.trailer.objectCount))
	}
}

func (p *binaryParser) parseDocument() (root cfValue, parseError error) {
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

	if len(p.buffer) < binaryHeaderSize+binaryTrailerSize {
		panic(errors.New("not enough data"))
	}

	if !bytes.HasPrefix(p.buffer, binaryMagic) {
		panic(errors.New("incomprehensible magic"))
	}

	version := int(((p.buffer[6] - '0') * 10) + (p.buffer[7] - '0'))
	if version > 1 {
		panic(fmt.Errorf("unexpected version %d", version))
	}

	p.trailerStart = uint64(len(p.buffer) - binaryTrailerSize)
	trailer := p.buffer[p.trailerStart:]
	p.trailer = binaryTrailer{
		offsetSize:       trailer[6],
		refSize:          trailer[7],
		objectCount:      binary.BigEndian.Uint64(trailer[8:]),
		rootIndex:        binary.BigEndian.Uint64(trailer[16:]),
		offsetTableStart: binary.BigEndian.Uint64(trailer[24:]),
	}

	p.validateTrailer()

	p.objects = make([]cfValue, p.trailer.objectCount)
	p.shapes = make([]valueShape, p.trailer.objectCount)
	p.budget = valueBudget(len(p.buffer))

	root = p.object(p.trailer.rootIndex)
	return root, parseError
}

// readSizedInt returns a 128-bit integer as low64, high64
func (p *binaryParser) readSizedInt(off uint64, nbytes int) (lo, hi, next uint64) {
	// Per comments in CoreFoundation, format version 00 requires that all
	// 1, 2 or 4-byte integers be interpreted as unsigned. 8-byte integers are
	// signed (always?) and therefore must be sign extended here.
	// negative 1, 2, or 4-byte integers are always emitted as 64-bit.
	switch nbytes {
	case 1:
		p.checkRange(off, 1)
		lo = uint64(p.buffer[off])
	case 2:
		p.checkRange(off, 2)
		lo = uint64(binary.BigEndian.Uint16(p.buffer[off:]))
	case 4:
		p.checkRange(off, 4)
		lo = uint64(binary.BigEndian.Uint32(p.buffer[off:]))
	case 8:
		p.checkRange(off, 8)
		lo = binary.BigEndian.Uint64(p.buffer[off:])
		if p.buffer[off]&0x80 != 0 {
			// sign extend if lo is signed
			hi = signedHighBits
		}
	case 16:
		p.checkRange(off, 16)
		lo, hi = binary.BigEndian.Uint64(p.buffer[off+8:]), binary.BigEndian.Uint64(p.buffer[off:])
	default:
		if nbytes > 8 {
			panic(errors.New("illegal integer size"))
		}
		// The integer is the low end of the eight bytes that stop where it stops.
		pad := uint64(8 - nbytes)
		if pad > off {
			panic(fmt.Errorf("integer read at offset 0x%x underflows buffer", off))
		}
		p.checkRange(off-pad, 8)
		lo = binary.BigEndian.Uint64(p.buffer[off-pad:]) & ((1 << uint64(nbytes*8)) - 1)
	}
	return lo, hi, off + uint64(nbytes)
}

func (p *binaryParser) readRef(off uint64) (index, next uint64) {
	index, _, next = p.readSizedInt(off, int(p.trailer.refSize))
	return index, next
}

func (p *binaryParser) object(index uint64) cfValue {
	if index >= p.trailer.objectCount {
		panic(fmt.Errorf("invalid object#%d (max %d)", index, p.trailer.objectCount))
	}

	if cached := p.objects[index]; cached != nil {
		return cached
	}

	off, _, _ := p.readSizedInt(p.trailer.offsetTableStart+(index*uint64(p.trailer.offsetSize)), int(p.trailer.offsetSize))
	if off > p.trailer.offsetTableStart-1 {
		panic(fmt.Errorf("object#%d starts beyond beginning of object table (0x%x, table@0x%x)", index, off, p.trailer.offsetTableStart))
	}

	value, shape := p.readObject(off)
	p.objects[index] = value
	p.shapes[index] = shape
	return value
}

// checkRange fails before a slice expression can: an out-of-range slice is a runtime panic, which Decode passes on.
func (p *binaryParser) checkRange(start, length uint64) {
	size := uint64(len(p.buffer))
	if start > size || length > size-start {
		panic(fmt.Errorf("read of %d bytes at offset 0x%x exceeds buffer length %d", length, start, size))
	}
}

// Objects that do not overlap hold fewer references and payload bytes than the document has bytes.
func (p *binaryParser) charge(n uint64) {
	p.work += n
	if p.work > p.budget {
		panic(errTooManyValues)
	}
}

func (p *binaryParser) enter(off uint64) {
	if len(p.enclosing) >= maxParseDepth {
		panic(errMaxDepthExceeded)
	}
	for _, outer := range p.enclosing {
		if outer == off {
			p.failSelfReference(off)
		}
	}
	p.enclosing = append(p.enclosing, off)
}

func (p *binaryParser) failSelfReference(off uint64) {
	var chain strings.Builder
	for _, outer := range p.enclosing {
		fmt.Fprintf(&chain, "0x%x > ", outer)
	}
	panic(fmt.Errorf("self-referential collection@0x%x (%s0x%x) cannot be deserialized", off, chain.String(), off))
}

func (p *binaryParser) leave() {
	p.enclosing = p.enclosing[:len(p.enclosing)-1]
}

func (p *binaryParser) readObject(off uint64) (cfValue, valueShape) {
	p.checkRange(off, 1)

	switch p.buffer[off] & 0xF0 {
	case markerDict:
		return p.readDict(off)
	case markerArray:
		return p.readArray(off)
	}
	return p.readScalar(off), valueShape{values: 1}
}

func (p *binaryParser) readScalar(off uint64) cfValue {
	marker := p.buffer[off]

	switch marker & 0xF0 {
	case markerNull:
		if marker == markerTrue || marker == markerFalse {
			return cfBool(marker == markerTrue)
		}
	case markerInt:
		lo, hi, _ := p.readInt(off)
		return &cfInteger{
			signed: hi == signedHighBits, // a signed integer is stored as a 128-bit integer with the top 64 bits set
			value:  lo,
		}
	case markerReal:
		switch 1 << (marker & 0x0F) {
		case 4:
			p.checkRange(off+1, 4)
			bits := binary.BigEndian.Uint32(p.buffer[off+1:])
			return &cfReal{is32: true, value: float64(math.Float32frombits(bits))}
		case 8:
			p.checkRange(off+1, 8)
			bits := binary.BigEndian.Uint64(p.buffer[off+1:])
			return &cfReal{value: math.Float64frombits(bits)}
		}
		panic(errors.New("illegal float size"))
	case markerDate:
		p.checkRange(off+1, 8)
		seconds := math.Float64frombits(binary.BigEndian.Uint64(p.buffer[off+1:])) + appleEpochOffset
		sec, frac := math.Modf(seconds)
		return cfDate(time.Unix(int64(sec), int64(frac*float64(time.Second))).In(time.UTC))
	case markerData:
		return cfData(p.readData(off))
	case markerASCII:
		return cfString(p.readASCII(off))
	case markerUTF16:
		return cfString(p.readUTF16(off))
	case markerUID: // Somehow different than int: low half is nbytes - 1 instead of log2(nbytes)
		lo, _, _ := p.readSizedInt(off+1, int(marker&0xF)+1)
		return cfUID(lo)
	}
	panic(fmt.Errorf("unexpected atom 0x%2.02x at offset 0x%x", marker, off))
}

func (p *binaryParser) readInt(off uint64) (lo, hi, next uint64) {
	marker := p.buffer[off]
	return p.readSizedInt(off+1, 1<<(marker&0xF))
}

func (p *binaryParser) readCount(off uint64) (count, start uint64) {
	marker := p.buffer[off]
	count = uint64(marker & 0x0F)
	if count != 0xF {
		return count, off + 1
	}

	count, _, start = p.readInt(off + 1)
	// A count cannot exceed the object region. Rejecting it here keeps the sums and products computed from it from wrapping.
	if count > p.trailer.offsetTableStart {
		panic(fmt.Errorf("element count %d at offset 0x%x exceeds object region (table@0x%x)", count, start, p.trailer.offsetTableStart))
	}
	return count, start
}

func (p *binaryParser) readData(off uint64) []byte {
	count, start := p.readCount(off)
	if start+count > p.trailer.offsetTableStart {
		panic(fmt.Errorf("data@0x%x too long (%v bytes, max is %v)", off, count, p.trailer.offsetTableStart-start))
	}
	p.charge(count)
	// The copy has no spare capacity, so appending to it cannot write into memory another reference sees.
	data := make([]byte, count)
	copy(data, p.buffer[start:])
	return data
}

func (p *binaryParser) readASCII(off uint64) string {
	count, start := p.readCount(off)
	if start+count > p.trailer.offsetTableStart {
		panic(fmt.Errorf("ascii string@0x%x too long (%v bytes, max is %v)", off, count, p.trailer.offsetTableStart-start))
	}

	p.charge(count)
	return string(p.buffer[start : start+count])
}

func (p *binaryParser) readUTF16(off uint64) string {
	count, start := p.readCount(off)
	size := count * 2
	if start+size > p.trailer.offsetTableStart {
		panic(fmt.Errorf("utf16 string@0x%x too long (%v bytes, max is %v)", off, size, p.trailer.offsetTableStart-start))
	}

	p.charge(size)
	units := make([]uint16, count)
	for i := range units {
		units[i] = binary.BigEndian.Uint16(p.buffer[start+uint64(i)*2:])
	}
	return string(utf16.Decode(units))
}

// Charging references as they are read bounds parsing; limiting the shape bounds unmarshaling.
func (p *binaryParser) readObjectList(off, count uint64) ([]cfValue, valueShape) {
	if off+(count*uint64(p.trailer.refSize)) > p.trailer.offsetTableStart {
		panic(fmt.Errorf("list@0x%x length (%v) puts its end beyond the offset table at 0x%x", off, count, p.trailer.offsetTableStart))
	}
	p.charge(count)
	objects := make([]cfValue, count)
	shape := valueShape{values: 1}

	next := off
	var index uint64
	for i := range objects {
		index, next = p.readRef(next)
		objects[i] = p.object(index)

		child := p.shapes[index]
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

func (p *binaryParser) readDict(off uint64) (*cfDict, valueShape) {
	p.enter(off)
	defer p.leave()

	// A dictionary is an object list of [keys... values...].
	count, start := p.readCount(off)
	objects, shape := p.readObjectList(start, count*2)

	keys := make([]string, count)
	for i := range keys {
		key, ok := objects[i].(cfString)
		if !ok {
			panic(fmt.Errorf("dictionary@0x%x contains non-string key at index %d", off, i))
		}
		keys[i] = string(key)
	}

	return &cfDict{
		keys:   keys,
		values: objects[count:],
	}, shape
}

func (p *binaryParser) readArray(off uint64) (*cfArray, valueShape) {
	p.enter(off)
	defer p.leave()

	// an array is just an object list
	count, start := p.readCount(off)
	objects, shape := p.readObjectList(start, count)
	return &cfArray{objects}, shape
}

func newBinaryParser(r io.Reader) *binaryParser {
	return &binaryParser{reader: r}
}
