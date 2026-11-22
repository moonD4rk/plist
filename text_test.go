package plist

import (
	"math"
	"reflect"
	"testing"
)

// The valid text test cases have been merged into the common/global test cases.

var textFormats = []int{OpenStepFormat, GNUStepFormat}

func TestTextStringsThatNeedQuotes(t *testing.T) {
	want := []string{"a;b", ";", "//line", "/*block", "/* block */", "//", "/*", "a//b", "a/*b", "/", "/a"}

	for _, format := range textFormats {
		t.Run(FormatNames[format], func(t *testing.T) {
			var list []string
			marshalThenUnmarshal(t, want, format, &list)
			if !reflect.DeepEqual(list, want) {
				t.Errorf("array decoded as %q, want %q", list, want)
			}

			for _, s := range want {
				var root string
				marshalThenUnmarshal(t, s, format, &root)
				if root != s {
					t.Errorf("root %q decoded as %q", s, root)
				}

				var dict map[string]string
				marshalThenUnmarshal(t, map[string]string{s: s}, format, &dict)
				if len(dict) != 1 || dict[s] != s {
					t.Errorf("dictionary of %q decoded as %q", s, dict)
				}
			}
		})
	}
}

func TestTextQuotedOutput(t *testing.T) {
	tests := []struct {
		value  any
		format int
		want   string
	}{
		{map[string]string{"k": "a;b"}, OpenStepFormat, `{k="a;b";}`},
		{map[string]string{"k": "a;b"}, GNUStepFormat, `{k="a;b";}`},
		{[]string{"//x", "/*x", "a//b"}, OpenStepFormat, `("//x","/*x",a//b,)`},
		{[]string{"//x", "/*x", "a//b"}, GNUStepFormat, `("//x","/*x",a//b,)`},
		{[]float64{1e6, 999999, 1.5, -2, 1e-7, 1e21}, OpenStepFormat, `("1e+06",999999,1.5,-2,1e-07,"1e+21",)`},
		{[]float64{1e6, 999999, 1.5, -2, 1e-7, 1e21}, GNUStepFormat, `(<*R1e+06>,<*R999999>,<*R1.5>,<*R-2>,<*R1e-07>,<*R1e+21>,)`},
		{math.Inf(1), OpenStepFormat, `"+Inf"`},
	}
	for _, tc := range tests {
		data, err := Marshal(tc.value, tc.format)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != tc.want {
			t.Errorf("%s: encoded %s, want %s", FormatNames[tc.format], data, tc.want)
		}
	}
}

func TestTextRealsRoundTrip(t *testing.T) {
	want := []float64{1e6, 123456789, 1e21, 1.7976931348623157e308, math.Inf(1), math.Inf(-1), 999999, 1.5, -2, 1e-7}

	for _, format := range textFormats {
		var got []float64
		marshalThenUnmarshal(t, want, format, &got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: decoded %v, want %v", FormatNames[format], got, want)
		}
	}
}

func TestTextArrayKeepsEmptyStrings(t *testing.T) {
	documents := map[string][]string{
		`(a, "", b)`: {"a", "", "b"},
		`("")`:       {""},
		`("", "")`:   {"", ""},
		`(a,"",b,)`:  {"a", "", "b"},
		`()`:         nil,
		`(,)`:        nil,
	}
	for document, want := range documents {
		var got []string
		if _, err := Unmarshal([]byte(document), &got); err != nil {
			t.Fatalf("%s: %v", document, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s decoded as %q, want %q", document, got, want)
		}
	}

	want := []string{"a", "", "b"}
	for _, format := range allFormats {
		var got []string
		marshalThenUnmarshal(t, want, format, &got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: decoded %q, want %q", FormatNames[format], got, want)
		}
	}
}

func TestTextQuotedStringEscapes(t *testing.T) {
	document := `"a\nb\t\"c\"\\d\101\x41\u0041e"`
	want := "a\nb\t\"c\"\\dAAAe"

	var got string
	if _, err := Unmarshal([]byte(document), &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("decoded %q, want %q", got, want)
	}
}
