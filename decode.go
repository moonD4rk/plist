package plist

import (
	"bufio"
	"bytes"
	"encoding"
	"errors"
	"fmt"
	"io"
	"reflect"
	"runtime"
	"time"
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
// an error. A binary property list has a budget, the larger of its size in bytes and 524288. It is an
// error if reading its objects takes more object references and bytes of strings and data than the
// budget, which happens only when objects overlap, or if it expands to more values than the budget
// once every reference to a shared object is followed. A binary property list that shares no
// containers stays within the budget whatever its size.
func Unmarshal(data []byte, v any) (format int, err error) {
	r := bytes.NewReader(data)
	dec := NewDecoder(r)
	err = dec.Decode(v)
	format = dec.Format
	return format, err
}

func settable(val reflect.Value) reflect.Value {
	if !val.CanSet() {
		panic(errNotSettable)
	}
	return val
}

// unmarshalInto reports a value that cannot be set as a fault of v, the value the caller passed in.
func (p *Decoder) unmarshalInto(pval cfValue, v any) {
	target := reflect.ValueOf(v)
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if err, ok := r.(error); !ok || !errors.Is(err, errNotSettable) {
			panic(r)
		}
		if !target.IsValid() {
			panic(&invalidDecodeTargetError{})
		}
		isNil := (target.Kind() == reflect.Ptr || target.Kind() == reflect.Map) && target.IsNil()
		panic(&invalidDecodeTargetError{typ: target.Type(), isNil: isNil})
	}()

	p.unmarshal(pval, target)
}

var (
	plistUnmarshalerType = reflect.TypeOf((*Unmarshaler)(nil)).Elem()
	textUnmarshalerType  = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
	uidType              = reflect.TypeOf(UID(0))
	stringType           = reflect.TypeOf("")
)

func isEmptyInterface(v reflect.Value) bool {
	return v.Kind() == reflect.Interface && v.NumMethod() == 0
}

func (p *Decoder) unmarshalPlistInterface(pval cfValue, unmarshalable Unmarshaler) {
	err := unmarshalable.UnmarshalPlist(func(i any) (err error) {
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
		p.unmarshalInto(pval, i)
		return err
	})
	if err != nil {
		panic(err)
	}
}

func (p *Decoder) unmarshalTextInterface(pval cfString, unmarshalable encoding.TextUnmarshaler) {
	err := unmarshalable.UnmarshalText([]byte(pval))
	if err != nil {
		panic(err)
	}
}

func (p *Decoder) unmarshalTime(pval cfDate, val reflect.Value) {
	settable(val).Set(reflect.ValueOf(time.Time(pval)))
}

func (p *Decoder) unmarshalLaxString(s string, val reflect.Value) {
	switch val.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i := mustParseInt(s, 10, 64)
		settable(val).SetInt(i)
		return
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		i := mustParseUint(s, 10, 64)
		settable(val).SetUint(i)
		return
	case reflect.Float32, reflect.Float64:
		f := mustParseFloat(s, 64)
		settable(val).SetFloat(f)
		return
	case reflect.Bool:
		b := mustParseBool(s)
		settable(val).SetBool(b)
		return
	case reflect.Struct:
		if val.Type() == timeType {
			t, err := time.Parse(textPlistTimeLayout, s)
			if err != nil {
				panic(err)
			}
			settable(val).Set(reflect.ValueOf(t.In(time.UTC)))
			return
		}
		fallthrough
	default:
		panic(&incompatibleDecodeTypeError{val.Type(), "string"})
	}
}

func (p *Decoder) unmarshal(pval cfValue, val reflect.Value) {
	if pval == nil {
		return
	}
	if !val.IsValid() {
		panic(errNotSettable)
	}

	for val.Kind() == reflect.Ptr {
		if val.IsNil() {
			settable(val).Set(reflect.New(val.Type().Elem()))
		}
		val = val.Elem()
	}

	if isEmptyInterface(val) {
		v := p.valueInterface(pval)
		settable(val).Set(reflect.ValueOf(v))
		return
	}

	incompatibleTypeError := &incompatibleDecodeTypeError{val.Type(), pval.typeName()}

	if receiver, can := implementsInterface(val, plistUnmarshalerType); can {
		u, _ := receiver.(Unmarshaler)
		p.unmarshalPlistInterface(pval, u)
		return
	}

	// A date goes into a time.Time only; it is never handed to UnmarshalText.
	if date, ok := pval.(cfDate); ok {
		if val.Type() == timeType {
			p.unmarshalTime(date, val)
			return
		}
		panic(incompatibleTypeError)
	}

	if val.Type() != timeType {
		if receiver, can := implementsInterface(val, textUnmarshalerType); can {
			if str, ok := pval.(cfString); ok {
				tu, _ := receiver.(encoding.TextUnmarshaler)
				p.unmarshalTextInterface(str, tu)
			} else {
				panic(incompatibleTypeError)
			}
			return
		}
	}

	typ := val.Type()

	switch pval := pval.(type) {
	case cfString:
		if val.Kind() == reflect.String {
			settable(val).SetString(string(pval))
			return
		}
		if p.lax {
			p.unmarshalLaxString(string(pval), val)
			return
		}

		panic(incompatibleTypeError)
	case *cfNumber:
		switch val.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			settable(val).SetInt(int64(pval.value))
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			settable(val).SetUint(pval.value)
		default:
			panic(incompatibleTypeError)
		}
	case *cfReal:
		if val.Kind() == reflect.Float32 || val.Kind() == reflect.Float64 {
			// TODO: Consider warning on a downcast (storing a 64-bit value in a 32-bit reflect)
			settable(val).SetFloat(pval.value)
		} else {
			panic(incompatibleTypeError)
		}
	case cfBoolean:
		if val.Kind() == reflect.Bool {
			settable(val).SetBool(bool(pval))
		} else {
			panic(incompatibleTypeError)
		}
	case cfData:
		if val.Kind() != reflect.Slice && val.Kind() != reflect.Array {
			panic(incompatibleTypeError)
		}

		if typ.Elem().Kind() != reflect.Uint8 {
			panic(incompatibleTypeError)
		}

		b := []byte(pval)
		switch val.Kind() {
		case reflect.Slice:
			settable(val).SetBytes(b)
		case reflect.Array:
			if val.Len() < len(b) {
				panic(fmt.Errorf("plist: attempted to unmarshal %d bytes into a byte array of size %d", len(b), val.Len()))
			}
			copy(settable(val).Bytes(), b)
		default:
			panic(incompatibleTypeError)
		}
	case cfUID:
		if val.Type() == uidType {
			settable(val).SetUint(uint64(pval))
		} else {
			switch val.Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				settable(val).SetInt(int64(pval))
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
				settable(val).SetUint(uint64(pval))
			default:
				panic(incompatibleTypeError)
			}
		}
	case *cfArray:
		p.unmarshalArray(pval, val)
	case *cfDictionary:
		p.unmarshalDictionary(pval, val)
	}
}

func (p *Decoder) unmarshalArray(a *cfArray, val reflect.Value) {
	var n int
	switch val.Kind() {
	case reflect.Slice:
		// Slice of element values.
		// Grow slice.
		settable(val)
		cnt := len(a.values) + val.Len()
		if cnt > val.Cap() {
			ncap := val.Cap()
			for ncap < cnt {
				ncap = growSliceCap(ncap)
			}
			grown := reflect.MakeSlice(val.Type(), val.Len(), ncap)
			reflect.Copy(grown, val)
			val.Set(grown)
		}
		n = val.Len()
		val.SetLen(cnt)
	case reflect.Array:
		if len(a.values) > val.Cap() {
			panic(fmt.Errorf("plist: attempted to unmarshal %d values into an array of size %d", len(a.values), val.Cap()))
		}
	default:
		panic(&incompatibleDecodeTypeError{val.Type(), a.typeName()})
	}

	// Recur to read element into slice.
	for _, sval := range a.values {
		p.unmarshal(sval, val.Index(n))
		n++
	}
}

func growSliceCap(c int) int {
	switch {
	case c == 0:
		return 4
	case c < 1024:
		return c * 2 // Double for small slices
	default:
		return c + c/4 // Increase by 25% for large slices
	}
}

func (p *Decoder) unmarshalDictionary(dict *cfDictionary, val reflect.Value) {
	typ := val.Type()
	switch val.Kind() {
	case reflect.Struct:
		tinfo, err := getTypeInfo(typ)
		if err != nil {
			panic(err)
		}

		entries := make(map[string]cfValue, len(dict.keys))
		for i, k := range dict.keys {
			sval := dict.values[i]
			entries[k] = sval
		}

		for _, finfo := range tinfo.fields {
			if ent, ok := entries[finfo.name]; ok {
				p.unmarshal(ent, finfo.valueForWriting(val))
			}
		}
	case reflect.Map:
		if val.IsNil() {
			settable(val).Set(reflect.MakeMap(typ))
		}

		if !stringType.ConvertibleTo(val.Type().Key()) {
			panic(fmt.Errorf("plist: attempt to decode dictionary into map with non-string key type `%v'", val.Type().Key()))
		}

		for i, k := range dict.keys {
			sval := dict.values[i]

			keyv := reflect.ValueOf(k).Convert(typ.Key())
			mapElem := reflect.New(typ.Elem()).Elem()

			p.unmarshal(sval, mapElem)
			val.SetMapIndex(keyv, mapElem)
		}
	default:
		panic(&incompatibleDecodeTypeError{typ, dict.typeName()})
	}
}

/* *Interface is modeled after encoding/json */
func (p *Decoder) valueInterface(pval cfValue) any {
	switch pval := pval.(type) {
	case cfString:
		return string(pval)
	case *cfNumber:
		if pval.signed {
			return int64(pval.value)
		}
		return pval.value
	case *cfReal:
		if pval.wide {
			return pval.value
		}
		return float32(pval.value)
	case cfBoolean:
		return bool(pval)
	case *cfArray:
		return p.arrayInterface(pval)
	case *cfDictionary:
		return p.dictionaryInterface(pval)
	case cfData:
		return []byte(pval)
	case cfDate:
		return time.Time(pval)
	case cfUID:
		return UID(pval)
	}
	return nil
}

func (p *Decoder) arrayInterface(a *cfArray) []any {
	out := make([]any, len(a.values))
	for i, subv := range a.values {
		out[i] = p.valueInterface(subv)
	}
	return out
}

func (p *Decoder) dictionaryInterface(dict *cfDictionary) map[string]any {
	out := make(map[string]any)
	for i, k := range dict.keys {
		subv := dict.values[i]
		out[k] = p.valueInterface(subv)
	}
	return out
}
