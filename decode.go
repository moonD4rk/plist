package plist

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"reflect"
	"runtime"
)

type parser interface {
	parseDocument() (cfValue, error)
}

// A Decoder reads a property list from an input stream.
type Decoder struct {
	// Format is the format of the property list that Decode parsed most recently, or InvalidFormat
	// before the first one. A Decode that fails to parse its input leaves Format unchanged.
	Format int

	reader io.ReadSeeker
	lax    bool
	target reflect.Value // the value given to Decode or to an UnmarshalPlist callback
}

// Decode reads one property list from the stream and stores it in the value pointed to by v,
// following the rules of Unmarshal.
//
// Decode always starts at the beginning of the stream and reads to its end: it seeks to offset 0
// first, wherever the stream is positioned. To read a property list that is embedded in a larger
// file, wrap that part of the file in an io.SectionReader.
//
// After Decode has parsed the document, the Decoder's Format field holds its format.
func (p *Decoder) Decode(v any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(runtime.Error); ok {
				panic(r)
			}
			if e, ok := r.(error); ok {
				err = e
			} else {
				panic(r)
			}
		}
	}()

	if _, err := p.reader.Seek(0, io.SeekStart); err != nil {
		return err
	}
	first, err := firstParser(p.reader)
	if err != nil {
		return err
	}
	if _, err := p.reader.Seek(0, io.SeekStart); err != nil {
		return err
	}

	var parser parser
	var pval cfValue
	switch first {
	case BinaryFormat:
		parser = newBplistParser(p.reader)
		pval, err = parser.parseDocument()
		if err != nil {
			// Had a bplist header, but still got an error: we have to die here.
			return err
		}
		p.Format = BinaryFormat
	case XMLFormat:
		parser = newXMLPlistParser(p.reader)
		pval, err = parser.parseDocument()
		if _, ok := err.(invalidPlistError); ok {
			// Rewind: the XML parser might have exhausted the file.
			if _, err := p.reader.Seek(0, io.SeekStart); err != nil {
				return err
			}
			pval, err = p.parseTextDocument()
		} else if err == nil {
			p.Format = XMLFormat
		}
		if err != nil {
			return err
		}
	default:
		pval, err = p.parseTextDocument()
		if err != nil {
			return err
		}
	}

	p.unmarshalInto(pval, v)
	return err
}

func (p *Decoder) parseTextDocument() (cfValue, error) {
	tp := newTextPlistParser(p.reader)
	pval, err := tp.parseDocument()
	if err != nil {
		return nil, err
	}
	p.Format = tp.format
	if p.Format == OpenStepFormat {
		// OpenStep property lists can only store strings,
		// so we have to turn on lax mode here for the unmarshal step later.
		p.lax = true
	}
	return pval, nil
}

var (
	bplistMagic = []byte("bplist")
	utf8BOM     = []byte{0xEF, 0xBB, 0xBF}
)

// XML is tried only for documents that start with '<': a text plist may hold XML tags inside its strings.
func firstParser(r io.Reader) (int, error) {
	br := bufio.NewReaderSize(r, 512)

	magic, err := br.Peek(len(bplistMagic))
	if err != nil && !errors.Is(err, io.EOF) {
		return InvalidFormat, err
	}
	if bytes.Equal(magic, bplistMagic) {
		return BinaryFormat, nil
	}

	if bom, _ := br.Peek(len(utf8BOM)); bytes.Equal(bom, utf8BOM) {
		_, _ = br.Discard(len(utf8BOM))
	}
	for {
		c, err := br.ReadByte()
		switch {
		case errors.Is(err, io.EOF):
			return OpenStepFormat, nil
		case err != nil:
			return InvalidFormat, err
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
		case c == '<':
			return XMLFormat, nil
		default:
			return OpenStepFormat, nil
		}
	}
}

// NewDecoder returns a Decoder that reads a property list from r.
// The stream must be seekable because detecting the format can take more than one pass over it.
func NewDecoder(r io.ReadSeeker) *Decoder {
	return &Decoder{Format: InvalidFormat, reader: r, lax: false}
}

// Unmarshal parses a property list document and stores the result in the value pointed to by v.
// It returns the format of the document, which it detects from the document itself.
//
// v must be a non-nil pointer. As an exception, a non-nil map can receive a dictionary.
// Unmarshal returns an error if it has to store a value and v gives it no way to do so.
//
// Unmarshal uses the inverse of the type encodings that Marshal uses, allocating maps, slices and
// pointers as necessary:
//
//   - A dictionary is stored in a struct or in a map whose key type is a string type. Keys are matched
//     to struct fields by the names Marshal would write, case-sensitively, and a key without a field
//     is ignored. Entries are added to a map that already has some.
//   - An array is stored in a slice or an array. Its elements are appended to the slice. An array
//     that is too short for them is an error; one that is longer keeps its remaining elements.
//   - Data is stored in a slice or an array of bytes. A binary property list can store equal data
//     once and refer to it from several places; the slices decoded from it then share memory, so
//     copy such a slice before changing its bytes.
//   - A date is stored in a time.Time.
//   - A string is stored in a string, or passed to UnmarshalText if the value implements
//     encoding.TextUnmarshaler.
//
// To decode into an empty interface value, Unmarshal replaces whatever the interface holds with one
// of the following:
//
//	string, bool
//	uint64, or int64 for negative integers
//	float64, or float32 for the 32-bit reals of binary property lists
//	time.Time, for dates
//	plist.UID for "CoreFoundation Keyed Archiver UIDs" (convertible to uint64)
//	[]byte, for plist data
//	[]any, for plist arrays
//	map[string]any, for plist dictionaries
//
// If a property list value is not appropriate for a given value type, Unmarshal aborts immediately and returns an error.
// The range of a number is not checked: an integer that does not fit the target type is truncated, and a real
// that does not fit a float32 becomes an infinity.
//
// As Go does not support 128-bit types, and we don't want to pretend we're giving the user integer types (as opposed to
// secretly passing them structs), Unmarshal will drop the high 64 bits of any 128-bit integers encoded in binary property lists.
// (This is important because CoreFoundation serializes some large 64-bit values as 128-bit values with an empty high half.)
//
// When Unmarshal encounters an OpenStep property list, it will enter a relaxed parsing mode: OpenStep property lists can only store
// plain old data as strings, so we will attempt to recover integer, floating-point, boolean and date values wherever they are necessary.
// (for example, if Unmarshal attempts to unmarshal an OpenStep property list into a time.Time, it will try to parse the string it
// receives as a time.) Integers must be decimal, and booleans are read by strconv.ParseBool.
//
// Unmarshal recognizes a binary property list by its magic number. It reads a document as XML if the
// first character after an optional byte order mark and white space is '<', and as OpenStep or GNUStep
// text otherwise, or if the first XML element is not one a property list has.
//
// Unmarshal limits the work a document can ask for. Containers nested deeper than about 128 levels are
// an error. So is a binary property list that holds more object references, or expands to more values
// once every reference to a shared object is followed, than the larger of its size in bytes and 524288.
// A binary property list that shares no containers stays within that limit whatever its size.
func Unmarshal(data []byte, v any) (format int, err error) {
	r := bytes.NewReader(data)
	dec := NewDecoder(r)
	err = dec.Decode(v)
	format = dec.Format
	return format, err
}
