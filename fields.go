package plist

import (
	"reflect"
	"strings"
	"sync"
)

func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Ptr:
		return v.IsNil()
	default:
		return false
	}
}

// A field is one struct field that is encoded and decoded, reached through index.
type field struct {
	name  string
	index []int

	// Bit i is set when the struct field at index[i] is tagged omitempty. A path through
	// embedded structs has one bit per level, so levels past the 64th lose their flag.
	omitEmptyMask uint64
}

type structFields []field

var fieldCache sync.Map // reflect.Type to structFields

// cachedFields returns the fields of the struct type typ that are encoded and decoded.
func cachedFields(typ reflect.Type) structFields {
	if cached, ok := fieldCache.Load(typ); ok {
		fields, _ := cached.(structFields)
		return fields
	}
	fields := fieldsOf(typ, nil)
	fieldCache.Store(typ, fields)
	return fields
}

// fieldsOf leaves out an embedded type found in embedding: its fields are already promoted from a shallower level.
func fieldsOf(typ reflect.Type, embedding []reflect.Type) structFields {
	embedding = append(embedding, typ)

	var fields structFields
	for i := 0; i < typ.NumField(); i++ {
		sf := typ.Field(i)
		if sf.PkgPath != "" || sf.Tag.Get("plist") == "-" {
			continue // unexported, or excluded with plist:"-"
		}

		f := newField(sf)
		if embedded, ok := embeddedStruct(sf); ok {
			if !containsType(embedding, embedded) {
				fields = fields.addEmbedded(f, fieldsOf(embedded, embedding))
			}
			continue
		}
		fields = fields.add(f)
	}
	return fields
}

func embeddedStruct(sf reflect.StructField) (reflect.Type, bool) {
	if !sf.Anonymous {
		return nil, false
	}
	typ := sf.Type
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	return typ, typ.Kind() == reflect.Struct
}

func containsType(types []reflect.Type, typ reflect.Type) bool {
	for _, t := range types {
		if t == typ {
			return true
		}
	}
	return false
}

func newField(sf reflect.StructField) field {
	f := field{name: sf.Name, index: sf.Index}

	options := strings.Split(sf.Tag.Get("plist"), ",")
	if options[0] != "" {
		f.name = options[0]
	}
	for _, option := range options[1:] {
		if option == "omitempty" {
			f.omitEmptyMask = 1 << uint(len(sf.Index)-1)
		}
	}
	return f
}

func (fields structFields) addEmbedded(outer field, inner structFields) structFields {
	for _, f := range inner {
		f.index = append(append([]int(nil), outer.index...), f.index...)
		f.omitEmptyMask = outer.omitEmptyMask | (f.omitEmptyMask << uint(len(outer.index)))
		fields = fields.add(f)
	}
	return fields
}

// add appends added unless a field of the same name is embedded less deeply; it replaces those that are not.
func (fields structFields) add(added field) structFields {
	kept := fields[:0:0]
	for _, f := range fields {
		if f.name != added.name {
			kept = append(kept, f)
			continue
		}
		if len(f.index) < len(added.index) {
			return fields
		}
	}
	return append(kept, added)
}

// fieldForDecode is v.FieldByIndex(f.index), except that it allocates nil embedded pointers on the way.
// It returns the zero Value if it cannot set one.
func (f field) fieldForDecode(v reflect.Value) reflect.Value {
	for i, x := range f.index {
		if i > 0 && v.Kind() == reflect.Ptr && v.Type().Elem().Kind() == reflect.Struct {
			if v.IsNil() {
				if !v.CanSet() {
					return reflect.Value{}
				}
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(x)
	}
	return v
}

// fieldForEncode is v.FieldByIndex(f.index), or the zero Value past a nil embedded pointer or an empty omitempty level.
func (f field) fieldForEncode(v reflect.Value) reflect.Value {
	for i, x := range f.index {
		if v.Kind() == reflect.Ptr && v.Type().Elem().Kind() == reflect.Struct {
			if v.IsNil() {
				return reflect.Value{}
			}
			v = v.Elem()
		}

		v = v.Field(x)

		if f.omitEmptyMask&(1<<uint(i)) != 0 && isEmptyValue(v) {
			return reflect.Value{}
		}
	}
	return v
}
