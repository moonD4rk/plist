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

// A Decoder reads a property list from an input stream.
type Decoder struct {
	// Format is the format of the property list that Decode parsed most recently, or InvalidFormat
	// before the first one. A Decode that fails to parse its input leaves Format unchanged.
	Format int

	reader        io.ReadSeeker
	coerceStrings bool
}

// Decode reads one property list from the stream and stores it in the value pointed to by v,
// following the rules of Unmarshal.
//
// Decode always starts at the beginning of the stream and reads to its end: it seeks to offset 0
// first, wherever the stream is positioned. To read a property list that is embedded in a larger
// file, wrap that part of the file in an io.SectionReader.
//
// After Decode has parsed the document, the Decoder's Format field holds its format.
func (d *Decoder) Decode(v any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(runtime.Error); ok {
				panic(r)
			}
			if rerr, ok := r.(error); ok {
				err = rerr
			} else {
				panic(r)
			}
		}
	}()

	if _, err := d.reader.Seek(0, io.SeekStart); err != nil {
		return err
	}
	first, err := firstParser(d.reader)
	if err != nil {
		return err
	}
	if _, err := d.reader.Seek(0, io.SeekStart); err != nil {
		return err
	}

	var root cfValue
	switch first {
	case BinaryFormat:
		root, err = newBplistParser(d.reader).parseDocument()
		if err != nil {
			// Had a bplist header, but still got an error: we have to die here.
			return err
		}
		d.Format = BinaryFormat
	case XMLFormat:
		root, err = newXMLPlistParser(d.reader).parseDocument()
		if _, ok := err.(invalidPlistError); ok {
			// Rewind: the XML parser might have exhausted the file.
			if _, err := d.reader.Seek(0, io.SeekStart); err != nil {
				return err
			}
			root, err = d.parseTextDocument()
		} else if err == nil {
			d.Format = XMLFormat
		}
		if err != nil {
			return err
		}
	default:
		root, err = d.parseTextDocument()
		if err != nil {
			return err
		}
	}

	d.unmarshalInto(root, v)
	return err
}

func (d *Decoder) parseTextDocument() (cfValue, error) {
	tp := newTextPlistParser(d.reader)
	root, err := tp.parseDocument()
	if err != nil {
		return nil, err
	}
	d.Format = tp.format
	if d.Format == OpenStepFormat {
		// OpenStep property lists can only store strings, so numbers, booleans and dates are parsed out of them.
		d.coerceStrings = true
	}
	return root, nil
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
	return &Decoder{Format: InvalidFormat, reader: r, coerceStrings: false}
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

func settable(dst reflect.Value) reflect.Value {
	if !dst.CanSet() {
		panic(errNotSettable)
	}
	return dst
}

// unmarshalInto reports a value that cannot be set as a fault of v, the value the caller passed in.
func (d *Decoder) unmarshalInto(src cfValue, v any) {
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
			panic(&invalidTargetError{})
		}
		isNil := (target.Kind() == reflect.Ptr || target.Kind() == reflect.Map) && target.IsNil()
		panic(&invalidTargetError{typ: target.Type(), isNil: isNil})
	}()

	d.unmarshal(src, target)
}

var (
	uidType    = reflect.TypeOf(UID(0))
	stringType = reflect.TypeOf("")
)

func isEmptyInterface(v reflect.Value) bool {
	return v.Kind() == reflect.Interface && v.NumMethod() == 0
}

func (d *Decoder) unmarshalUnmarshaler(src cfValue, u Unmarshaler) {
	err := u.UnmarshalPlist(func(i any) (err error) {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(runtime.Error); ok {
					panic(r)
				}
				if rerr, ok := r.(error); ok {
					err = rerr
				} else {
					panic(r)
				}
			}
		}()
		d.unmarshalInto(src, i)
		return err
	})
	if err != nil {
		panic(err)
	}
}

func (d *Decoder) unmarshalTextUnmarshaler(text cfString, tu encoding.TextUnmarshaler) {
	err := tu.UnmarshalText([]byte(text))
	if err != nil {
		panic(err)
	}
}

func (d *Decoder) unmarshalTime(date cfDate, dst reflect.Value) {
	settable(dst).Set(reflect.ValueOf(time.Time(date)))
}

func (d *Decoder) unmarshalCoerced(s string, dst reflect.Value) {
	switch dst.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i := mustParseInt(s, 10, 64)
		settable(dst).SetInt(i)
		return
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		i := mustParseUint(s, 10, 64)
		settable(dst).SetUint(i)
		return
	case reflect.Float32, reflect.Float64:
		f := mustParseFloat(s, 64)
		settable(dst).SetFloat(f)
		return
	case reflect.Bool:
		b := mustParseBool(s)
		settable(dst).SetBool(b)
		return
	case reflect.Struct:
		if dst.Type() == timeType {
			t, err := time.Parse(textPlistTimeLayout, s)
			if err != nil {
				panic(err)
			}
			settable(dst).Set(reflect.ValueOf(t.In(time.UTC)))
			return
		}
		fallthrough
	default:
		panic(&unmarshalTypeError{dst.Type(), "string"})
	}
}

func (d *Decoder) unmarshal(src cfValue, dst reflect.Value) {
	if src == nil {
		return
	}
	if !dst.IsValid() {
		panic(errNotSettable)
	}

	for dst.Kind() == reflect.Ptr {
		if dst.IsNil() {
			settable(dst).Set(reflect.New(dst.Type().Elem()))
		}
		dst = dst.Elem()
	}

	if isEmptyInterface(dst) {
		v := d.valueInterface(src)
		settable(dst).Set(reflect.ValueOf(v))
		return
	}

	incompatibleTypeError := &unmarshalTypeError{dst.Type(), src.typeName()}

	if u, ok := implementation[Unmarshaler](dst); ok {
		d.unmarshalUnmarshaler(src, u)
		return
	}

	// A date goes into a time.Time only; it is never handed to UnmarshalText.
	if date, ok := src.(cfDate); ok {
		if dst.Type() == timeType {
			d.unmarshalTime(date, dst)
			return
		}
		panic(incompatibleTypeError)
	}

	if dst.Type() != timeType {
		if tu, ok := implementation[encoding.TextUnmarshaler](dst); ok {
			if str, ok := src.(cfString); ok {
				d.unmarshalTextUnmarshaler(str, tu)
			} else {
				panic(incompatibleTypeError)
			}
			return
		}
	}

	typ := dst.Type()

	switch src := src.(type) {
	case cfString:
		if dst.Kind() == reflect.String {
			settable(dst).SetString(string(src))
			return
		}
		if d.coerceStrings {
			d.unmarshalCoerced(string(src), dst)
			return
		}

		panic(incompatibleTypeError)
	case *cfInteger:
		switch dst.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			settable(dst).SetInt(int64(src.value))
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			settable(dst).SetUint(src.value)
		default:
			panic(incompatibleTypeError)
		}
	case *cfReal:
		if dst.Kind() == reflect.Float32 || dst.Kind() == reflect.Float64 {
			// TODO: Consider warning on a downcast (storing a 64-bit value in a 32-bit reflect)
			settable(dst).SetFloat(src.value)
		} else {
			panic(incompatibleTypeError)
		}
	case cfBool:
		if dst.Kind() == reflect.Bool {
			settable(dst).SetBool(bool(src))
		} else {
			panic(incompatibleTypeError)
		}
	case cfData:
		if dst.Kind() != reflect.Slice && dst.Kind() != reflect.Array {
			panic(incompatibleTypeError)
		}

		if typ.Elem().Kind() != reflect.Uint8 {
			panic(incompatibleTypeError)
		}

		b := []byte(src)
		switch dst.Kind() {
		case reflect.Slice:
			settable(dst).SetBytes(b)
		case reflect.Array:
			if dst.Len() < len(b) {
				panic(fmt.Errorf("plist: attempted to unmarshal %d bytes into a byte array of size %d", len(b), dst.Len()))
			}
			copy(settable(dst).Bytes(), b)
		default:
			panic(incompatibleTypeError)
		}
	case cfUID:
		if dst.Type() == uidType {
			settable(dst).SetUint(uint64(src))
		} else {
			switch dst.Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				settable(dst).SetInt(int64(src))
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
				settable(dst).SetUint(uint64(src))
			default:
				panic(incompatibleTypeError)
			}
		}
	case *cfArray:
		d.unmarshalArray(src, dst)
	case *cfDict:
		d.unmarshalDict(src, dst)
	}
}

func (d *Decoder) unmarshalArray(a *cfArray, dst reflect.Value) {
	var n int
	switch dst.Kind() {
	case reflect.Slice:
		// reflect.Value.Grow would not do: it carries the elements between len and cap over to the new array.
		n = dst.Len()
		total := n + len(a.values)
		if total > settable(dst).Cap() {
			capacity := dst.Cap()
			for capacity < total {
				capacity = growSliceCap(capacity)
			}
			grown := reflect.MakeSlice(dst.Type(), n, capacity)
			reflect.Copy(grown, dst)
			dst.Set(grown)
		}
		dst.SetLen(total)
	case reflect.Array:
		if len(a.values) > dst.Cap() {
			panic(fmt.Errorf("plist: attempted to unmarshal %d values into an array of size %d", len(a.values), dst.Cap()))
		}
	default:
		panic(&unmarshalTypeError{dst.Type(), a.typeName()})
	}

	// Recur to read element into slice.
	for _, sval := range a.values {
		d.unmarshal(sval, dst.Index(n))
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

func (d *Decoder) unmarshalDict(dict *cfDict, dst reflect.Value) {
	typ := dst.Type()
	switch dst.Kind() {
	case reflect.Struct:
		entries := make(map[string]cfValue, len(dict.keys))
		for i, k := range dict.keys {
			sval := dict.values[i]
			entries[k] = sval
		}

		for _, f := range cachedFields(typ) {
			if ent, ok := entries[f.name]; ok {
				d.unmarshal(ent, f.fieldForDecode(dst))
			}
		}
	case reflect.Map:
		if dst.IsNil() {
			settable(dst).Set(reflect.MakeMap(typ))
		}

		if !stringType.ConvertibleTo(dst.Type().Key()) {
			panic(fmt.Errorf("plist: attempt to decode dictionary into map with non-string key type `%v'", dst.Type().Key()))
		}

		for i, k := range dict.keys {
			sval := dict.values[i]

			keyv := reflect.ValueOf(k).Convert(typ.Key())
			mapElem := reflect.New(typ.Elem()).Elem()

			d.unmarshal(sval, mapElem)
			dst.SetMapIndex(keyv, mapElem)
		}
	default:
		panic(&unmarshalTypeError{typ, dict.typeName()})
	}
}

/* *Interface is modeled after encoding/json */
func (d *Decoder) valueInterface(src cfValue) any {
	switch src := src.(type) {
	case cfString:
		return string(src)
	case *cfInteger:
		if src.signed {
			return int64(src.value)
		}
		return src.value
	case *cfReal:
		if src.is32 {
			return float32(src.value)
		}
		return src.value
	case cfBool:
		return bool(src)
	case *cfArray:
		return d.arrayInterface(src)
	case *cfDict:
		return d.dictInterface(src)
	case cfData:
		return []byte(src)
	case cfDate:
		return time.Time(src)
	case cfUID:
		return UID(src)
	}
	return nil
}

func (d *Decoder) arrayInterface(a *cfArray) []any {
	out := make([]any, len(a.values))
	for i, subv := range a.values {
		out[i] = d.valueInterface(subv)
	}
	return out
}

func (d *Decoder) dictInterface(dict *cfDict) map[string]any {
	out := make(map[string]any)
	for i, k := range dict.keys {
		subv := dict.values[i]
		out[k] = d.valueInterface(subv)
	}
	return out
}
