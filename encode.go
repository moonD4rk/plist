package plist

import (
	"bytes"
	"encoding"
	"errors"
	"fmt"
	"io"
	"reflect"
	"runtime"
	"time"
)

type generator interface {
	generateDocument(cfValue)
	Indent(string)
}

// An Encoder writes a property list to an output stream.
type Encoder struct {
	writer io.Writer
	format int

	indent string
}

// Encode writes the property list encoding of v to the stream.
//
// Each call writes one complete document. Documents written to the same stream run together
// and cannot be told apart when the stream is read back.
func (e *Encoder) Encode(v any) (err error) {
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

	root := e.marshal(reflect.ValueOf(v))
	if root == nil {
		panic(errors.New("plist: no root element to encode"))
	}

	var g generator
	switch e.format {
	case XMLFormat:
		g = newXMLPlistGenerator(e.writer)
	case BinaryFormat, AutomaticFormat:
		g = newBplistGenerator(e.writer)
	case OpenStepFormat, GNUStepFormat:
		g = newTextPlistGenerator(e.writer, e.format)
	default:
		panic(fmt.Errorf("plist: invalid encoding format %d", e.format))
	}
	g.Indent(e.indent)
	g.generateDocument(root)
	return err
}

// Indent turns on pretty-printing for the XML and text property list formats. The binary format ignores it.
// Each element begins on a new line and is preceded by one copy of indent for each level it is nested.
// indent is written to the output as it is, so it should consist of white space only.
func (e *Encoder) Indent(indent string) {
	e.indent = indent
}

// NewEncoder returns an Encoder that writes an XML property list to w.
func NewEncoder(w io.Writer) *Encoder {
	return NewEncoderForFormat(w, XMLFormat)
}

// NewEncoderForFormat returns an Encoder that writes a property list to w in the specified format.
// Pass AutomaticFormat to allow the library to choose the best encoding (currently BinaryFormat).
func NewEncoderForFormat(w io.Writer, format int) *Encoder {
	return &Encoder{
		writer: w,
		format: format,
	}
}

// NewBinaryEncoder returns an Encoder that writes a binary property list to w.
func NewBinaryEncoder(w io.Writer) *Encoder {
	return NewEncoderForFormat(w, BinaryFormat)
}

// Marshal returns the property list encoding of v in the specified format.
//
// Pass AutomaticFormat to allow the library to choose the best encoding (currently BinaryFormat).
//
// Marshal traverses the value v recursively.
// The property list formats bear no representation for nil values: a nil pointer, a nil interface value and
// a nil result of MarshalPlist are an error as the root value and are left out anywhere else.
// A struct field or map entry holding one is not written, and an array element is skipped.
// (The OpenStep and GNUStep formats write an empty array element in its place, which this package skips when decoding.)
//
// Strings, integers of varying size, floats and booleans are encoded as the property list type of the same kind.
// The OpenStep format has only strings and stores them all as such.
// Strings bearing non-ASCII runes will be encoded differently depending upon the property list format:
// UTF-8 for XML property lists and UTF-16 for binary property lists.
// The XML format replaces characters that XML cannot represent with U+FFFD.
//
// time.Time values are encoded as dates and UID values as UIDs.
//
// A value that implements Marshaler is encoded as the value its MarshalPlist method returns.
// Otherwise, a value that implements encoding.TextMarshaler is encoded as the string its MarshalText method returns.
// A method with a pointer receiver is called only for a pointer or an addressable value.
//
// Slice and Array values are encoded as property list arrays, except for slices and arrays
// of bytes (of any type whose underlying type is uint8), which are encoded as data.
//
// Map values encode as dictionaries. The map's key type must be a string type;
// there is no provision for encoding non-string dictionary keys.
//
// Struct values are encoded as dictionaries, with only exported fields being serialized.
// The keys of a dictionary are written in sorted order.
// Struct field encoding may be influenced with the use of tags.
// The tag format is:
//
//	`plist:"<key>[,flags...]"`
//
// The following flags are supported:
//
//	omitempty    Only include the field if it is not empty. The empty values are false, 0,
//	             a nil pointer, a nil interface value, and any array, slice, map or string of length zero.
//
// If the key is "-", the field is ignored.
//
// Anonymous struct fields are encoded as if their exported fields were exposed via the outer struct,
// provided the type of the anonymous field is itself exported. A key in the tag of an anonymous struct field is ignored.
//
// Pointer values encode as the value pointed to.
//
// Channel, complex, function and unsafe.Pointer values cannot be encoded, and neither can a value held in an
// interface type that has methods. Any attempt to do so causes Marshal to return an error.
//
// Marshal does not detect cyclic data structures. Encoding one recurses until the program runs out of stack.
func Marshal(v any, format int) ([]byte, error) {
	return MarshalIndent(v, format, "")
}

// MarshalIndent works like Marshal, but each property list element
// begins on a new line and is preceded by one copy of indent for each level it is nested, as described for Encoder.Indent.
func MarshalIndent(v any, format int, indent string) ([]byte, error) {
	buf := &bytes.Buffer{}
	enc := NewEncoderForFormat(buf, format)
	enc.Indent(indent)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var (
	marshalerType     = reflect.TypeOf((*Marshaler)(nil)).Elem()
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	timeType          = reflect.TypeOf((*time.Time)(nil)).Elem()
)

// A method declared on the element type cannot be called through a nil pointer, so the pointer is left out.
func isNilValueReceiver(val reflect.Value, interfaceType reflect.Type) bool {
	for val.Kind() == reflect.Interface && !val.IsNil() {
		val = val.Elem()
	}
	return val.Kind() == reflect.Ptr && val.IsNil() && val.Type().Elem().Implements(interfaceType)
}

// implementation returns val as a T, or a pointer to val when val is addressable and the pointer is a T.
func implementation[T any](val reflect.Value) (T, bool) {
	if val.CanInterface() {
		if impl, ok := val.Interface().(T); ok {
			return impl, true
		}
	}

	if val.CanAddr() {
		if ptr := val.Addr(); ptr.CanInterface() {
			if impl, ok := ptr.Interface().(T); ok {
				return impl, true
			}
		}
	}

	var none T
	return none, false
}

func (e *Encoder) marshalMarshaler(m Marshaler) cfValue {
	value, err := m.MarshalPlist()
	if err != nil {
		panic(err)
	}
	return e.marshal(reflect.ValueOf(value))
}

// marshalTextMarshaler marshals a TextMarshaler to a plist string.
func (e *Encoder) marshalTextMarshaler(tm encoding.TextMarshaler) cfValue {
	s, err := tm.MarshalText()
	if err != nil {
		panic(err)
	}
	return cfString(s)
}

// marshalStruct marshals a reflected struct value to a plist dictionary
func (e *Encoder) marshalStruct(typ reflect.Type, val reflect.Value) cfValue {
	fields := cachedFields(typ)

	dict := &cfDict{
		keys:   make([]string, 0, len(fields)),
		values: make([]cfValue, 0, len(fields)),
	}
	for _, f := range fields {
		value := f.fieldForEncode(val)
		if !value.IsValid() {
			continue
		}
		encoded := e.marshal(value)
		if encoded == nil {
			continue
		}
		dict.keys = append(dict.keys, f.name)
		dict.values = append(dict.values, encoded)
	}

	return dict
}

func (e *Encoder) marshalTime(val reflect.Value) cfValue {
	t, _ := val.Interface().(time.Time)
	return cfDate(t)
}

func innermostValue(val reflect.Value) reflect.Value {
	for val.Kind() == reflect.Ptr || (val.Kind() == reflect.Interface && val.NumMethod() == 0) {
		val = val.Elem()
	}
	return val
}

func (e *Encoder) marshal(val reflect.Value) cfValue {
	if !val.IsValid() {
		return nil
	}

	if isNilValueReceiver(val, marshalerType) {
		return nil
	}
	if m, ok := implementation[Marshaler](val); ok {
		return e.marshalMarshaler(m)
	}

	// time.Time implements TextMarshaler, but we need to store it in RFC3339
	if val.Type() == timeType {
		return e.marshalTime(val)
	}
	if val.Kind() == reflect.Ptr || (val.Kind() == reflect.Interface && val.NumMethod() == 0) {
		ival := innermostValue(val)
		if ival.IsValid() && ival.Type() == timeType {
			return e.marshalTime(ival)
		}
	}

	// Check for text marshaler.
	if isNilValueReceiver(val, textMarshalerType) {
		return nil
	}
	if tm, ok := implementation[encoding.TextMarshaler](val); ok {
		return e.marshalTextMarshaler(tm)
	}

	// Descend into pointers or interfaces
	val = innermostValue(val)

	// We got this far and still may have an invalid anything or nil ptr/interface
	if !val.IsValid() || ((val.Kind() == reflect.Ptr || val.Kind() == reflect.Interface) && val.IsNil()) {
		return nil
	}

	typ := val.Type()

	if typ == uidType {
		return cfUID(val.Uint())
	}

	if val.Kind() == reflect.Struct {
		return e.marshalStruct(typ, val)
	}

	switch val.Kind() {
	case reflect.String:
		return cfString(val.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return &cfInteger{signed: true, value: uint64(val.Int())}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return &cfInteger{signed: false, value: val.Uint()}
	case reflect.Float32:
		return &cfReal{is32: true, value: val.Float()}
	case reflect.Float64:
		return &cfReal{value: val.Float()}
	case reflect.Bool:
		return cfBool(val.Bool())
	case reflect.Slice, reflect.Array:
		if typ.Elem().Kind() == reflect.Uint8 {
			var b []byte
			if val.CanAddr() && val.Kind() == reflect.Slice {
				// arrays may be addressable but do not support .Bytes
				b = val.Bytes()
			} else {
				// reflect.Copy needs identical element types, and the element may be a named byte type.
				dst := reflect.MakeSlice(reflect.SliceOf(typ.Elem()), val.Len(), val.Len())
				reflect.Copy(dst, val)
				b = dst.Bytes()
			}
			return cfData(b)
		}
		values := make([]cfValue, val.Len())
		for i, length := 0, val.Len(); i < length; i++ {
			if elem := e.marshal(val.Index(i)); elem != nil {
				values[i] = elem
			}
		}
		return &cfArray{values}
	case reflect.Map:
		if typ.Key().Kind() != reflect.String {
			panic(&unsupportedTypeError{typ})
		}

		l := val.Len()
		dict := &cfDict{
			keys:   make([]string, 0, l),
			values: make([]cfValue, 0, l),
		}
		for _, keyv := range val.MapKeys() {
			if elem := e.marshal(val.MapIndex(keyv)); elem != nil {
				dict.keys = append(dict.keys, keyv.String())
				dict.values = append(dict.values, elem)
			}
		}
		return dict
	default:
		panic(&unsupportedTypeError{typ})
	}
}
