package plist

import (
	"reflect"
	"testing"
	"time"
)

func TestInvalidMarshal(t *testing.T) {
	tests := []struct {
		Name  string
		Thing any
	}{
		{"Function", func() {}},
		{"Nil", nil},
		{"Map with integer keys", map[int]string{1: "hi"}},
		{"Channel", make(chan int)},
	}

	for _, v := range tests {
		t.Run(v.Name, func(t *testing.T) {
			data, err := Marshal(v.Thing, OpenStepFormat)
			if err == nil {
				t.Fatalf("expected error; got plist data: %x", data)
			} else {
				t.Log(err)
			}
		})
	}
}

type Cat struct{}

func (c *Cat) MarshalPlist() (any, error) {
	return "cat", nil
}

func TestInterfaceMarshal(t *testing.T) {
	var c Cat
	b, err := Marshal(&c, XMLFormat)
	if err != nil {
		t.Log(err)
	} else if len(b) == 0 {
		t.Log("expect non-zero data")
	}
}

func TestInterfaceFieldMarshal(t *testing.T) {
	type X struct {
		C any // C's type does not implement Marshaler
	}
	x := &X{
		C: &Cat{}, // C's value implements Marshaler
	}

	b, err := Marshal(x, XMLFormat)
	if err != nil {
		t.Log(err)
	} else if len(b) == 0 {
		t.Log("expect non-zero data")
	}
}

func TestMarshalInterfaceFieldPtrTime(t *testing.T) {
	type X struct {
		C any // C's type is unknown
	}

	sentinelTime := time.Date(2013, 11, 27, 0, 34, 0, 0, time.UTC)
	x := &X{
		C: &sentinelTime,
	}

	e := &Encoder{}
	rval := reflect.ValueOf(x)
	cf := e.marshal(rval)

	if dict, ok := cf.(*cfDictionary); ok {
		if _, ok := dict.values[0].(cfDate); !ok {
			t.Error("inner value is not a cfDate")
		}
	} else {
		t.Error("failed to marshal toplevel dictionary (?)")
	}
}

type Dog struct {
	Name string
}

type Animal any

func TestInterfaceSliceMarshal(t *testing.T) {
	x := make([]Animal, 0)
	x = append(x, &Dog{Name: "dog"})

	b, err := Marshal(x, XMLFormat)
	if err != nil {
		t.Error(err)
	} else if len(b) == 0 {
		t.Error("expect non-zero data")
	}
}

func TestInterfaceGeneralSliceMarshal(t *testing.T) {
	x := make([]any, 0) // accept any type
	x = append(x, &Dog{Name: "dog"}, "a string", 1, true)

	b, err := Marshal(x, XMLFormat)
	if err != nil {
		t.Error(err)
	} else if len(b) == 0 {
		t.Error("expect non-zero data")
	}
}

type valueMarshaler struct{ S string }

func (v valueMarshaler) MarshalPlist() (any, error) { return v.S, nil }

type valueTextMarshaler struct{ S string }

func (v valueTextMarshaler) MarshalText() ([]byte, error) { return []byte(v.S), nil }

type nilMarshaler struct{}

func (nilMarshaler) MarshalPlist() (any, error) { return nil, nil }

func marshalThenUnmarshal(t *testing.T, value any, format int, out any) {
	t.Helper()
	data, err := Marshal(value, format)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := Unmarshal(data, out); err != nil {
		t.Fatalf("Unmarshal(%q): %v", data, err)
	}
}

func TestMarshalNilArrayElement(t *testing.T) {
	values := map[string]any{
		"nil interface":  []any{"a", nil, "b"},
		"nil pointer":    []*string{new(string), nil},
		"nil marshaler":  []any{"a", nilMarshaler{}},
		"only nil":       []any{nil},
		"nil in a field": struct{ List []any }{[]any{nil, "a"}},
	}
	for name, value := range values {
		for _, format := range []int{XMLFormat, BinaryFormat} {
			t.Run(name+"/"+FormatNames[format], func(t *testing.T) {
				var xml, got any
				marshalThenUnmarshal(t, value, XMLFormat, &xml)
				marshalThenUnmarshal(t, value, format, &got)
				if !reflect.DeepEqual(got, xml) {
					t.Errorf("decoded %#v, want %#v", got, xml)
				}
			})
		}
	}
}

func TestMarshalNilStructField(t *testing.T) {
	type record struct {
		Name      string
		Pointer   *int
		Interface any
		Time      *time.Time
		Plist     *valueMarshaler
		Text      *valueTextMarshaler
		Nothing   nilMarshaler
	}
	want := map[string]string{"Name": "n"}

	for _, format := range allFormats {
		t.Run(FormatNames[format], func(t *testing.T) {
			var got map[string]string
			marshalThenUnmarshal(t, record{Name: "n"}, format, &got)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("decoded %#v, want %#v", got, want)
			}
		})
	}
}

func TestMarshalNilPointerToValueReceiver(t *testing.T) {
	values := map[string]any{
		"time":           []*time.Time{nil},
		"Marshaler":      []*valueMarshaler{nil},
		"TextMarshaler":  []*valueTextMarshaler{nil},
		"map value":      map[string]*valueTextMarshaler{"a": nil},
		"pointer in any": []any{(*valueMarshaler)(nil)},
	}
	for name, value := range values {
		for _, format := range allFormats {
			t.Run(name+"/"+FormatNames[format], func(t *testing.T) {
				if _, err := Marshal(value, format); err != nil {
					t.Fatal(err)
				}
			})
		}
	}

	for _, format := range allFormats {
		if _, err := Marshal((*valueMarshaler)(nil), format); err == nil {
			t.Errorf("%s: Marshal of a nil root pointer succeeded, want an error", FormatNames[format])
		}
	}
}

func TestMarshalNilPointerReceiverIsCalled(t *testing.T) {
	var got map[string]string
	marshalThenUnmarshal(t, struct{ C *Cat }{}, XMLFormat, &got)
	if want := map[string]string{"C": "cat"}; !reflect.DeepEqual(got, want) {
		t.Errorf("decoded %#v, want %#v", got, want)
	}
}

type SelfEmbedded struct {
	*SelfEmbedded
	Name string
}

type MutualA struct {
	*MutualB
	A string
}

type MutualB struct {
	*MutualA
	B string
}

func TestMarshalSelfEmbeddedStruct(t *testing.T) {
	for _, format := range allFormats {
		t.Run(FormatNames[format], func(t *testing.T) {
			value := SelfEmbedded{SelfEmbedded: &SelfEmbedded{Name: "inner"}, Name: "outer"}
			var got map[string]string
			marshalThenUnmarshal(t, value, format, &got)
			if want := map[string]string{"Name": "outer"}; !reflect.DeepEqual(got, want) {
				t.Errorf("decoded %#v, want %#v", got, want)
			}

			var back SelfEmbedded
			marshalThenUnmarshal(t, value, format, &back)
			if back.Name != "outer" || back.SelfEmbedded != nil {
				t.Errorf("decoded %#v, want only Name set", back)
			}
		})
	}
}

func TestMarshalMutuallyEmbeddedStructs(t *testing.T) {
	want := map[string]string{"A": "a", "B": "b"}

	var fromA, fromB map[string]string
	marshalThenUnmarshal(t, MutualA{MutualB: &MutualB{B: "b"}, A: "a"}, XMLFormat, &fromA)
	marshalThenUnmarshal(t, MutualB{MutualA: &MutualA{A: "a"}, B: "b"}, XMLFormat, &fromB)
	if !reflect.DeepEqual(fromA, want) || !reflect.DeepEqual(fromB, want) {
		t.Errorf("decoded %#v and %#v, want %#v from both", fromA, fromB, want)
	}

	var back MutualB
	if _, err := Unmarshal([]byte(`{A=a;B=b;}`), &back); err != nil {
		t.Fatal(err)
	}
	if back.B != "b" || back.MutualA == nil || back.A != "a" {
		t.Errorf("decoded %#v, want A and B set", back)
	}
}

func TestMarshalNilEmbeddedPointer(t *testing.T) {
	type Inner struct{ I string }
	type outer struct {
		*Inner
		O string
	}
	want := map[string]string{"O": "o"}

	for _, format := range allFormats {
		t.Run(FormatNames[format], func(t *testing.T) {
			var got map[string]string
			marshalThenUnmarshal(t, outer{O: "o"}, format, &got)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("decoded %#v, want %#v", got, want)
			}
		})
	}
}

type namedByte uint8

func TestMarshalNamedByteElements(t *testing.T) {
	want := []byte{1, 2, 3}
	values := map[string]any{
		"slice":              []namedByte{1, 2, 3},
		"array":              [3]namedByte{1, 2, 3},
		"pointer to slice":   &[]namedByte{1, 2, 3},
		"pointer to array":   &[3]namedByte{1, 2, 3},
		"unnamed byte array": [3]byte{1, 2, 3},
	}
	for name, value := range values {
		for _, format := range allFormats {
			t.Run(name+"/"+FormatNames[format], func(t *testing.T) {
				var got []byte
				marshalThenUnmarshal(t, value, format, &got)
				if !reflect.DeepEqual(got, want) {
					t.Errorf("decoded %v, want %v", got, want)
				}
			})
		}
	}
}
