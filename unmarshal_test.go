package plist

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

type CustomDate struct{}

func (cd *CustomDate) UnmarshalPlist(unmarshal func(any) error) error { return nil }

func TestCustomDateUnmarshal(t *testing.T) {
	input := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
    <date>2003-02-03T09:00:00.00Z</date>
</plist>`

	var custom CustomDate
	if _, err := Unmarshal([]byte(input), &custom); err != nil {
		t.Error(err)
	}
}

func TestInvalidMapKeyTypeUnmarshal(t *testing.T) {
	m := make(map[int]string)
	dict := &cfDict{
		keys: []string{"1", "2"},
		values: []cfValue{
			cfString("first"),
			cfString("second"),
		},
	}

	var caught bool
	{
		defer func() {
			if e := recover(); e != nil {
				t.Log("error:", e)
				caught = true
			}
		}()

		d := &Decoder{}
		d.unmarshalDict(dict, reflect.ValueOf(m))
	}

	if !caught {
		t.Fail()
	}
}

func TestValidButAliasedMapKeyTypeUnmarshal(t *testing.T) {
	type sortaString string
	m := make(map[sortaString]string)
	dict := &cfDict{
		keys: []string{"1", "2"},
		values: []cfValue{
			cfString("first"),
			cfString("second"),
		},
	}

	var caught bool
	{
		defer func() {
			if e := recover(); e != nil {
				t.Error("error:", e)
				caught = true
			}
		}()

		d := &Decoder{}
		d.unmarshalDict(dict, reflect.ValueOf(m))
	}

	if caught {
		t.Fail()
	}
}

type valueSink struct{ dst *string }

func (s valueSink) UnmarshalPlist(unmarshal func(any) error) error { return unmarshal(s.dst) }

type badCallback struct{ arg any }

type EmbeddedName struct{ Name string }

type embedsName struct{ *EmbeddedName }

func (b *badCallback) UnmarshalPlist(unmarshal func(any) error) error { return unmarshal(b.arg) }

func TestUnmarshalInvalidTarget(t *testing.T) {
	type record struct{ Name string }
	var nilMap map[string]string
	var nilPointer *record

	tests := []struct {
		document string
		target   any
		want     string
	}{
		{`{Name=n;}`, nil, "plist: cannot decode into nil"},
		{`{Name=n;}`, record{}, "plist: cannot decode into non-pointer plist.record"},
		{`{Name=n;}`, nilMap, "plist: cannot decode into nil map[string]string"},
		{`{Name=n;}`, nilPointer, "plist: cannot decode into nil *plist.record"},
		{`{Name=n;}`, struct{ Name *string }{}, "plist: cannot decode into non-pointer struct { Name *string }"},
		{`{Name=n;}`, embedsName{}, "plist: cannot decode into non-pointer plist.embedsName"},
		{`{Name=n;}`, &badCallback{}, "plist: cannot decode into nil"},
		{`{Name=n;}`, &badCallback{record{}}, "plist: cannot decode into non-pointer plist.record"},
		{`{Name=n;}`, &badCallback{nilPointer}, "plist: cannot decode into nil *plist.record"},
		{`<string>s</string>`, "", "plist: cannot decode into non-pointer string"},
		{`<integer>1</integer>`, 0, "plist: cannot decode into non-pointer int"},
		{`<real>1</real>`, 0.5, "plist: cannot decode into non-pointer float64"},
		{`<true/>`, false, "plist: cannot decode into non-pointer bool"},
		{`<array><string>s</string></array>`, []string{"a", "b"}, "plist: cannot decode into non-pointer []string"},
		{`<array><string>s</string></array>`, [2]string{}, "plist: cannot decode into non-pointer [2]string"},
		{`<data>AQI=</data>`, []byte{0, 0}, "plist: cannot decode into non-pointer []uint8"},
		{`<data>AQI=</data>`, [2]byte{}, "plist: cannot decode into non-pointer [2]uint8"},
		{`<date>2020-01-01T00:00:00Z</date>`, time.Time{}, "plist: cannot decode into non-pointer time.Time"},
		{`<dict><key>CF$UID</key><integer>1</integer></dict>`, UID(0), "plist: cannot decode into non-pointer plist.UID"},
	}
	for _, tc := range tests {
		for _, format := range allFormats {
			var value any
			if _, err := Unmarshal([]byte(tc.document), &value); err != nil {
				t.Fatal(err)
			}
			document, err := Marshal(value, format)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Unmarshal(document, tc.target); err == nil || err.Error() != tc.want {
				t.Errorf("%s into %#v: error = %v, want %s", document, tc.target, err, tc.want)
			}
		}
	}
}

func TestUnmarshalWithoutPointer(t *testing.T) {
	document := []byte(`<dict><key>Name</key><string>n</string></dict>`)

	byValue := map[string]string{}
	if _, err := Unmarshal(document, byValue); err != nil || byValue["Name"] != "n" {
		t.Errorf("map by value: decoded %q with error %v", byValue, err)
	}

	var name string
	if _, err := Unmarshal([]byte(`<string>n</string>`), valueSink{&name}); err != nil || name != "n" {
		t.Errorf("Unmarshaler by value: decoded %q with error %v", name, err)
	}

	holder := struct{ Name *string }{new(string)}
	if _, err := Unmarshal(document, holder); err != nil || *holder.Name != "n" {
		t.Errorf("struct holding a pointer: decoded %q with error %v", *holder.Name, err)
	}

	if _, err := Unmarshal(document, struct{ Other string }{}); err != nil {
		t.Errorf("struct by value with nothing to store: %v", err)
	}

	format, err := Unmarshal(document, nil)
	if format != XMLFormat || err == nil {
		t.Errorf("nil target: format %s with error %v, want XML and an error", FormatNames[format], err)
	}
}

func TestUnmarshalNamedByteElements(t *testing.T) {
	want := []namedByte{1, 2, 3}

	for _, format := range allFormats {
		t.Run(FormatNames[format], func(t *testing.T) {
			var slice []namedByte
			marshalThenUnmarshal(t, want, format, &slice)
			if !reflect.DeepEqual(slice, want) {
				t.Errorf("slice decoded as %v, want %v", slice, want)
			}

			var array [4]namedByte
			marshalThenUnmarshal(t, want, format, &array)
			if array != [4]namedByte{1, 2, 3, 0} {
				t.Errorf("array decoded as %v, want [1 2 3 0]", array)
			}

			var field struct{ Colors [3]namedByte }
			marshalThenUnmarshal(t, map[string]any{"Colors": want}, format, &field)
			if field.Colors != [3]namedByte{1, 2, 3} {
				t.Errorf("field decoded as %v, want [1 2 3]", field.Colors)
			}
		})
	}
}

type concurrentCallback struct{ first, second string }

func (c *concurrentCallback) UnmarshalPlist(unmarshal func(any) error) error {
	targets := []*string{&c.first, &c.second}
	errs := make([]error, len(targets))

	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target *string) {
			defer wg.Done()
			errs[i] = unmarshal(target)
		}(i, target)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func TestUnmarshalCallbackFromGoroutines(t *testing.T) {
	var got concurrentCallback
	if _, err := Unmarshal([]byte(`<string>s</string>`), &got); err != nil {
		t.Fatal(err)
	}
	if got.first != "s" || got.second != "s" {
		t.Errorf("decoded %q and %q, want \"s\" twice", got.first, got.second)
	}
}
