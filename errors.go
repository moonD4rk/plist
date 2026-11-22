package plist

import (
	"errors"
	"fmt"
	"reflect"
)

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

type incompatibleDecodeTypeError struct {
	dest reflect.Type
	src  string // type name (from cfValue)
}

func (u *incompatibleDecodeTypeError) Error() string {
	return fmt.Sprintf("plist: type mismatch: tried to decode plist type `%v' into value of type `%v'", u.src, u.dest)
}

// invalidDecodeTargetError names the value given to Unmarshal, not the value inside it that could not be set.
type invalidDecodeTargetError struct {
	typ   reflect.Type // nil for an untyped nil target
	isNil bool
}

func (e *invalidDecodeTargetError) Error() string {
	switch {
	case e.typ == nil:
		return "plist: cannot decode into nil"
	case e.isNil:
		return "plist: cannot decode into nil " + e.typ.String()
	default:
		return "plist: cannot decode into non-pointer " + e.typ.String()
	}
}

var errNotSettable = errors.New("plist: decode target cannot be set")
