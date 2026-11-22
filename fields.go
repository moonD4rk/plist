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

// structFields holds details for the plist representation of a type.
type structFields struct {
	fields []field
}

// field holds details for the plist representation of a single field.
type field struct {
	idx  []int
	name string

	// omitEmptyMask stores, for each entry in idx, whether at that level the user had specified
	// omitempty. This matters for anonymous embedded structs, where the index path to a given field
	// may traverse different struct types
	//
	// For example, given struct S{ *I } and struct I{ V int }, the path to V is [0,0].
	// If S.I were marked `omitempty`, we would need to record that omitempty was seen at the first index entry.
	//    *I,         int
	// [   0,           0 ]
	// [omit, do not omit ]
	//
	// As an optimization, we store it as a bit field. This means anonymous embedded structs more than 64 entries
	// may forget their omitempty states.
	omitEmptyMask uint64
}

var (
	fieldCache     = make(map[reflect.Type]*structFields)
	fieldCacheLock sync.RWMutex
)

// cachedFields returns the typeInfo structure with details necessary
// for marshaling and unmarshaling typ.
func cachedFields(typ reflect.Type) (*structFields, error) {
	return fieldsWithin(typ, nil)
}

// fieldsWithin stops at a type in embedding: its fields are already promoted from a shallower level.
func fieldsWithin(typ reflect.Type, embedding []reflect.Type) (*structFields, error) {
	fieldCacheLock.RLock()
	tinfo, ok := fieldCache[typ]
	fieldCacheLock.RUnlock()
	if ok {
		return tinfo, nil
	}
	tinfo = &structFields{}
	if typ.Kind() == reflect.Struct {
		embedding = append(embedding, typ)
		n := typ.NumField()
		for i := 0; i < n; i++ {
			f := typ.Field(i)
			if f.PkgPath != "" || f.Tag.Get("plist") == "-" {
				continue // unexported, or excluded with plist:"-"
			}

			finfo, err := newField(typ, &f)
			if err != nil {
				return nil, err
			}

			// For embedded structs, embed its fields.
			if f.Anonymous {
				t := f.Type
				if t.Kind() == reflect.Ptr {
					t = t.Elem()
				}
				if t.Kind() == reflect.Struct {
					if err := addEmbeddedFields(typ, tinfo, finfo, t, embedding); err != nil {
						return nil, err
					}
					continue
				}
			}

			// Add the field if it doesn't conflict with other fields.
			if err := addField(typ, tinfo, finfo); err != nil {
				return nil, err
			}
		}
	}
	// A nested result may lack fields cut off by an outer type in embedding, so only top-level results are cached.
	if len(embedding) <= 1 {
		fieldCacheLock.Lock()
		fieldCache[typ] = tinfo
		fieldCacheLock.Unlock()
	}
	return tinfo, nil
}

func addEmbeddedFields(typ reflect.Type, tinfo *structFields, outer *field, embedded reflect.Type, embedding []reflect.Type) error {
	if containsType(embedding, embedded) {
		return nil
	}
	inner, err := fieldsWithin(embedded, embedding)
	if err != nil {
		return err
	}
	for _, innerFinfo := range inner.fields {
		innerFinfo.idx = append(outer.idx, innerFinfo.idx...)
		innerFinfo.omitEmptyMask = outer.omitEmptyMask | (innerFinfo.omitEmptyMask << uint(len(outer.idx)))
		if err := addField(typ, tinfo, &innerFinfo); err != nil {
			return err
		}
	}
	return nil
}

func containsType(types []reflect.Type, typ reflect.Type) bool {
	for _, t := range types {
		if t == typ {
			return true
		}
	}
	return false
}

// newField builds and returns a fieldInfo for f.
func newField(typ reflect.Type, f *reflect.StructField) (*field, error) {
	finfo := &field{idx: f.Index}

	tag := f.Tag.Get("plist")

	// Parse flags.
	tokens := strings.Split(tag, ",")
	tag = tokens[0]
	if len(tokens) > 1 {
		tag = tokens[0]
		for _, flag := range tokens[1:] {
			switch flag {
			case "omitempty":
				finfo.omitEmptyMask = 1 << uint(len(f.Index)-1)
			}
		}
	}

	if tag == "" {
		// If the name part of the tag is completely empty,
		// use the field name
		finfo.name = f.Name
		return finfo, nil
	}

	finfo.name = tag
	return finfo, nil
}

// addField adds newf unless a field of the same name is embedded less deeply; it replaces those that are not.
func addField(typ reflect.Type, tinfo *structFields, newf *field) error {
	var conflicts []int
	// First, figure all conflicts. Most working code will have none.
	for i := range tinfo.fields {
		oldf := &tinfo.fields[i]
		if newf.name == oldf.name {
			conflicts = append(conflicts, i)
		}
	}

	// Without conflicts, add the new field and return.
	if conflicts == nil {
		tinfo.fields = append(tinfo.fields, *newf)
		return nil
	}

	// If any conflict is shallower, ignore the new field.
	// This matches the Go field resolution on embedding.
	for _, i := range conflicts {
		if len(tinfo.fields[i].idx) < len(newf.idx) {
			return nil
		}
	}

	// Otherwise, the new field is shallower, and thus takes precedence,
	// so drop the conflicting fields from tinfo and append the new one.
	for c := len(conflicts) - 1; c >= 0; c-- {
		i := conflicts[c]
		copy(tinfo.fields[i:], tinfo.fields[i+1:])
		tinfo.fields = tinfo.fields[:len(tinfo.fields)-1]
	}
	tinfo.fields = append(tinfo.fields, *newf)
	return nil
}

// fieldForDecode returns v's field value corresponding to finfo.
// It's equivalent to v.FieldByIndex(finfo.idx), but initializes
// and dereferences pointers as necessary. It returns the zero Value
// if it cannot initialize a nil pointer on the way.
func (finfo *field) fieldForDecode(v reflect.Value) reflect.Value {
	for i, x := range finfo.idx {
		if i > 0 {
			t := v.Type()
			if t.Kind() == reflect.Ptr && t.Elem().Kind() == reflect.Struct {
				if v.IsNil() {
					if !v.CanSet() {
						return reflect.Value{}
					}
					v.Set(reflect.New(v.Type().Elem()))
				}
				v = v.Elem()
			}
		}
		v = v.Field(x)
	}
	return v
}

// fieldForEncode is v.FieldByIndex(finfo.idx), or the zero Value past a nil embedded pointer or an empty omitempty level.
func (finfo *field) fieldForEncode(v reflect.Value) reflect.Value {
	for i, x := range finfo.idx {
		t := v.Type()
		if t.Kind() == reflect.Ptr && t.Elem().Kind() == reflect.Struct {
			if v.IsNil() {
				return reflect.Value{}
			}
			v = v.Elem()
		}

		v = v.Field(x)

		if (finfo.omitEmptyMask&(1<<uint(i))) != 0 && isEmptyValue(v) {
			return reflect.Value{}
		}
	}
	return v
}
