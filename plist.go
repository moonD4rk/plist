package plist

import (
	"reflect"
)

// Property list format constants
const (
	// InvalidFormat is the format reported for a document that could not be parsed.
	InvalidFormat int = 0

	// AutomaticFormat lets the Encoder choose the output format (currently BinaryFormat).
	AutomaticFormat = 0

	// XMLFormat is Apple's XML property list format.
	XMLFormat = 1
	// BinaryFormat is Apple's binary property list format, version 00.
	BinaryFormat = 2
	// OpenStepFormat is the plain text format of NeXTSTEP and OpenStep, also used by .strings files.
	// It has no numbers, booleans or dates: every scalar is a string.
	OpenStepFormat = 3
	// GNUStepFormat is the OpenStep format with GNUstep's typed integers, reals, booleans and dates.
	// A text document that uses none of those is reported as OpenStepFormat.
	GNUStepFormat = 4
)

// FormatNames maps each format constant to a name for display.
var FormatNames = map[int]string{
	InvalidFormat:  "unknown/invalid",
	XMLFormat:      "XML",
	BinaryFormat:   "Binary",
	OpenStepFormat: "OpenStep",
	GNUStepFormat:  "GNUStep",
}

type unknownTypeError struct {
	typ reflect.Type
}

func (u *unknownTypeError) Error() string {
	return "plist: can't marshal value of type " + u.typ.String()
}

type invalidPlistError struct {
	format string
	err    error
}

func (e invalidPlistError) Error() string {
	s := "plist: invalid " + e.format + " property list"
	if e.err != nil {
		s += ": " + e.err.Error()
	}
	return s
}

type plistParseError struct {
	format string
	err    error
}

func (e plistParseError) Error() string {
	s := "plist: error parsing " + e.format + " property list"
	if e.err != nil {
		s += ": " + e.err.Error()
	}
	return s
}

// A UID is an object reference as used by keyed archives (NSKeyedArchiver).
// The binary format has a UID type of its own; the other formats write a UID as a dictionary
// with the single key CF$UID, and Unmarshal reads such a dictionary back as a UID.
//
// When decoding, a UID may be stored in an integer type and an integer in a UID.
type UID uint64

// Marshaler is the interface implemented by types that can marshal themselves into valid
// property list objects. The returned value is marshaled in place of the original value
// implementing Marshaler. A nil result is treated like a nil pointer: the value is left out.
//
// If an error is returned by MarshalPlist, marshaling stops and the error is returned.
type Marshaler interface {
	MarshalPlist() (any, error)
}

// Unmarshaler is the interface implemented by types that can unmarshal themselves from
// property list objects. The UnmarshalPlist method receives a function that may
// be called to unmarshal the original property list value into a field or variable.
// The function takes the same kinds of target as Unmarshal, so it needs a non-nil pointer.
//
// It is safe to call the unmarshal function more than once. Passing it the receiver itself
// calls UnmarshalPlist again and recurses without end.
type Unmarshaler interface {
	UnmarshalPlist(unmarshal func(any) error) error
}
