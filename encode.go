package plist

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"runtime"
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
func (p *Encoder) Encode(v any) (err error) {
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

	pval := p.marshal(reflect.ValueOf(v))
	if pval == nil {
		panic(errors.New("plist: no root element to encode"))
	}

	var g generator
	switch p.format {
	case XMLFormat:
		g = newXMLPlistGenerator(p.writer)
	case BinaryFormat, AutomaticFormat:
		g = newBplistGenerator(p.writer)
	case OpenStepFormat, GNUStepFormat:
		g = newTextPlistGenerator(p.writer, p.format)
	default:
		panic(fmt.Errorf("plist: invalid encoding format %d", p.format))
	}
	g.Indent(p.indent)
	g.generateDocument(pval)
	return err
}

// Indent turns on pretty-printing for the XML and text property list formats. The binary format ignores it.
// Each element begins on a new line and is preceded by one copy of indent for each level it is nested.
// indent is written to the output as it is, so it should consist of white space only.
func (p *Encoder) Indent(indent string) {
	p.indent = indent
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
