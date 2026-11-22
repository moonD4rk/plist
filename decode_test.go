package plist

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
)

func TestLaxDecode(t *testing.T) {
	laxTestDataStringsOnly := `{B=1;D="2013-11-27 00:34:00 +0000";I64=1;F64="3.0";U64=2;}`
	d := LaxTestData{}
	buf := bytes.NewReader([]byte(laxTestDataStringsOnly))
	decoder := NewDecoder(buf)
	decoder.coerceStrings = true
	err := decoder.Decode(&d)
	if err != nil {
		t.Error(err.Error())
	}

	if d != laxTestData {
		t.Logf("Expected: %#v", laxTestData)
		t.Logf("Received: %#v", d)
		t.Fail()
	}
}

func TestIllegalLaxDecode(t *testing.T) {
	i := int64(0)
	u := uint64(0)
	f := float64(0)
	b := false
	plists := []struct {
		pl string
		d  any
	}{
		{"<string>abc</string>", &i},
		{"<string>abc</string>", &u},
		{"<string>def</string>", &f},
		{"<string>ghi</string>", &b},
		{"<string>jkl</string>", []byte{0x00}},
	}

	for _, plist := range plists {
		buf := bytes.NewReader([]byte(plist.pl))
		decoder := NewDecoder(buf)
		decoder.coerceStrings = true
		err := decoder.Decode(plist.d)
		t.Logf("Error: %v", err)
		if err == nil {
			t.Error("Expected error, received nothing.")
		}
	}
}

func TestIllegalDecode(t *testing.T) {
	i := int64(0)
	b := false
	plists := []struct {
		pl string
		d  any
	}{
		{"<string>abc</string>", &i},
		{"<data>ABC=</data>", &i},
		{"<real>34.1</real>", &i},
		{"<true>def</true>", &i},
		{"<date>2010-01-01T00:00:00Z</date>", &i},
		{"<integer>0</integer>", &b},
		{"<array><integer>0</integer></array>", &b},
		{"<dict><key>a</key><integer>0</integer></dict>", &b},
		{"<array><true/><true/><true/></array>", &[1]int{1}},
		{"<data>SGVsbG8=</data>", &[3]byte{}},
	}

	for _, plist := range plists {
		buf := bytes.NewReader([]byte(plist.pl))
		decoder := NewDecoder(buf)
		err := decoder.Decode(plist.d)
		t.Logf("Error: %v", err)
		if err == nil {
			t.Error("Expected error, received nothing.")
		}
	}
}

func TestDecode(t *testing.T) {
	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			expVal := test.DecodeValue
			if expVal == nil {
				expVal = test.Value
			}

			expReflect := reflect.ValueOf(expVal)
			if !expReflect.IsValid() || isEmptyInterface(expReflect) {
				return
			}
			if expReflect.Kind() == reflect.Ptr || expReflect.Kind() == reflect.Interface {
				// Unbox pointer for comparison's sake
				expReflect = expReflect.Elem()
			}
			expVal = expReflect.Interface()

			results := make(map[int]any)
			for fmt, doc := range test.Documents {
				if test.SkipDecode[fmt] {
					return
				}
				t.Run(FormatNames[fmt], func(t *testing.T) {
					val := reflect.New(expReflect.Type()).Interface()
					_, err := Unmarshal(doc, val)
					if err != nil {
						t.Error(err)
					}

					valReflect := reflect.ValueOf(val)
					if valReflect.Kind() == reflect.Ptr || valReflect.Kind() == reflect.Interface {
						// Unbox pointer for comparison's sake
						valReflect = valReflect.Elem()
						val = valReflect.Interface()
					}

					results[fmt] = val
					if !reflect.DeepEqual(expVal, val) {
						t.Logf("Expected: %#v\n", expVal)
						t.Logf("Received: %#v\n", val)
						t.Fail()
					}
				})
			}

			if results[BinaryFormat] != nil && results[XMLFormat] != nil {
				if !reflect.DeepEqual(results[BinaryFormat], results[XMLFormat]) {
					t.Log("Binary and XML decoding yielded different values.")
					t.Log("Binary:", results[BinaryFormat])
					t.Log("XML   :", results[XMLFormat])
					t.Fail()
				}
			}
		})
	}
}

func TestInterfaceDecode(t *testing.T) {
	var xval any
	buf := bytes.NewReader([]byte{98, 112, 108, 105, 115, 116, 48, 48, 214, 1, 13, 17, 21, 25, 27, 2, 14, 18, 22, 26, 28, 88, 105, 110, 116, 97, 114, 114, 97, 121, 170, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 16, 1, 16, 8, 16, 16, 16, 32, 16, 64, 16, 2, 16, 9, 16, 17, 16, 33, 16, 65, 86, 102, 108, 111, 97, 116, 115, 162, 15, 16, 34, 66, 0, 0, 0, 35, 64, 80, 0, 0, 0, 0, 0, 0, 88, 98, 111, 111, 108, 101, 97, 110, 115, 162, 19, 20, 9, 8, 87, 115, 116, 114, 105, 110, 103, 115, 162, 23, 24, 92, 72, 101, 108, 108, 111, 44, 32, 65, 83, 67, 73, 73, 105, 0, 72, 0, 101, 0, 108, 0, 108, 0, 111, 0, 44, 0, 32, 78, 22, 117, 76, 84, 100, 97, 116, 97, 68, 1, 2, 3, 4, 84, 100, 97, 116, 101, 51, 65, 184, 69, 117, 120, 0, 0, 0, 8, 21, 30, 41, 43, 45, 47, 49, 51, 53, 55, 57, 59, 61, 68, 71, 76, 85, 94, 97, 98, 99, 107, 110, 123, 142, 147, 152, 157, 0, 0, 0, 0, 0, 0, 1, 1, 0, 0, 0, 0, 0, 0, 0, 29, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 166})
	decoder := NewDecoder(buf)
	err := decoder.Decode(&xval)
	if err != nil {
		t.Log("Error:", err)
		t.Fail()
	}
}

func TestFormatDetection(t *testing.T) {
	type formatTest struct {
		expectedFormat int
		data           []byte
	}
	plists := []formatTest{
		{BinaryFormat, []byte{98, 112, 108, 105, 115, 116, 48, 48, 85, 72, 101, 108, 108, 111, 8, 0, 0, 0, 0, 0, 0, 1, 1, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 14}},
		{XMLFormat, []byte(`<string>&lt;*I3&gt;</string>`)},
		{InvalidFormat, []byte(`bplist00`)}, // Looks like a binary property list, and bplist does not have fallbacks(!)
		{OpenStepFormat, []byte(`(1,2,3,4,5)`)},
		{OpenStepFormat, []byte(`<abab>`)},
		{GNUStepFormat, []byte(`(1,2,<*I3>)`)},
		{InvalidFormat, []byte{0x00}}, // This isn't a valid property list of any sort.
	}

	for i, fmttest := range plists {
		fmt, err := Unmarshal(fmttest.data, nil)
		if fmt != fmttest.expectedFormat {
			t.Errorf("plist %d: Wanted %s, received %s.", i, FormatNames[fmttest.expectedFormat], FormatNames[fmt])
		}
		if err != nil {
			t.Logf("plist %d: Error: %v", i, err)
		}
	}
}

func ExampleDecoder_Decode() {
	type sparseBundleHeader struct {
		InfoDictionaryVersion string `plist:"CFBundleInfoDictionaryVersion"`
		BandSize              uint64 `plist:"band-size"`
		BackingStoreVersion   int    `plist:"bundle-backingstore-version"`
		DiskImageBundleType   string `plist:"diskimage-bundle-type"`
		Size                  uint64 `plist:"size"`
	}

	buf := bytes.NewReader([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
	<dict>
		<key>CFBundleInfoDictionaryVersion</key>
		<string>6.0</string>
		<key>band-size</key>
		<integer>8388608</integer>
		<key>bundle-backingstore-version</key>
		<integer>1</integer>
		<key>diskimage-bundle-type</key>
		<string>com.apple.diskimage.sparsebundle</string>
		<key>size</key>
		<integer>4398046511104</integer>
	</dict>
</plist>`))

	var data sparseBundleHeader
	decoder := NewDecoder(buf)
	err := decoder.Decode(&data)
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println(data)

	// Output: {6.0 8388608 1 com.apple.diskimage.sparsebundle 4398046511104}
}

// chunkedReader returns at most n bytes per Read, modeling a streaming source
// (socket, pipe, TLS/gzip boundary) that does not fill a 6-byte buffer in one
// call. It supports the Seek(0,0) rewind the decoder uses for format sniffing.
type chunkedReader struct {
	data []byte
	pos  int
	n    int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := r.n
	if n > len(p) {
		n = len(p)
	}
	if r.pos+n > len(r.data) {
		n = len(r.data) - r.pos
	}
	copy(p, r.data[r.pos:r.pos+n])
	r.pos += n
	return n, nil
}

func (r *chunkedReader) Seek(offset int64, _ int) (int64, error) {
	r.pos = int(offset)
	return offset, nil
}

// A valid binary plist delivered through a reader that returns few bytes per
// Read must still be detected as Binary, not misrouted to the text parser.
func TestShortReadFormatDetection(t *testing.T) {
	bin, err := Marshal(map[string]any{"hello": "world"}, BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []int{1, 2, 3, 5} {
		var out map[string]any
		dec := NewDecoder(&chunkedReader{data: bin, n: chunk})
		if err := dec.Decode(&out); err != nil {
			t.Fatalf("chunk=%d: decode failed: %v", chunk, err)
		}
		if dec.Format != BinaryFormat {
			t.Fatalf("chunk=%d: format=%d, want BinaryFormat(%d)", chunk, dec.Format, BinaryFormat)
		}
		if out["hello"] != "world" {
			t.Fatalf("chunk=%d: got %v, want map[hello:world]", chunk, out)
		}
	}
}

func TestTextDocumentHoldingXMLTags(t *testing.T) {
	tests := []struct {
		document string
		want     map[string]string
	}{
		{`{a = "<true/>";}`, map[string]string{"a": "<true/>"}},
		{`{k="<true/>";}`, map[string]string{"k": "<true/>"}},
		{`"msg" = "Enter a <string> value";`, map[string]string{"msg": "Enter a <string> value"}},
		{"/* <plist> */ a = b;", map[string]string{"a": "b"}},
		{`{a = "<plist><dict/></plist>";}`, map[string]string{"a": "<plist><dict/></plist>"}},
	}
	for _, tc := range tests {
		var got map[string]string
		format, err := Unmarshal([]byte(tc.document), &got)
		if err != nil {
			t.Errorf("%s: %v", tc.document, err)
			continue
		}
		if format != OpenStepFormat || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s decoded as %s %q, want OpenStep %q", tc.document, FormatNames[format], got, tc.want)
		}
	}
}

func TestXMLDocumentStart(t *testing.T) {
	const document = `<plist version="1.0"><string>x</string></plist>`

	for _, prefix := range []string{"", " \t\r\n", "\xEF\xBB\xBF", "\xEF\xBB\xBF\n", xmlPreamble, "\xEF\xBB\xBF" + xmlPreamble, "<!-- c -->"} {
		var got string
		format, err := Unmarshal([]byte(prefix+document), &got)
		if err != nil || format != XMLFormat || got != "x" {
			t.Errorf("prefix %q: decoded %s %q with error %v, want XML \"x\"", prefix, FormatNames[format], got, err)
		}
	}

	for _, prefix := range []string{"x", "junk ", "{", "\x00"} {
		var got any
		if _, err := Unmarshal([]byte(prefix+document), &got); err == nil {
			t.Errorf("prefix %q: decoded %#v, want an error", prefix, got)
		}
	}
}

type seekFailer struct {
	*bytes.Reader
	seeks   int
	failing int
	err     error
}

func (r *seekFailer) Seek(offset int64, whence int) (int64, error) {
	r.seeks++
	if r.seeks == r.failing {
		return 0, r.err
	}
	return r.Reader.Seek(offset, whence)
}

func TestDecodeReturnsSeekError(t *testing.T) {
	want := errors.New("seek failed")
	for failing := 1; failing <= 3; failing++ {
		r := &seekFailer{Reader: bytes.NewReader([]byte(`<abab>`)), failing: failing, err: want}
		var out any
		if err := NewDecoder(r).Decode(&out); !errors.Is(err, want) {
			t.Errorf("seek %d failing: Decode error = %v with value %#v, want %v", failing, err, out, want)
		}
	}
}

func TestDecodeStartsAtBeginningOfStream(t *testing.T) {
	want := map[string]int{"answer": 42}

	for _, format := range allFormats {
		t.Run(FormatNames[format], func(t *testing.T) {
			data, err := Marshal(want, format)
			if err != nil {
				t.Fatal(err)
			}
			r := bytes.NewReader(data)
			if _, err := r.Seek(5, io.SeekStart); err != nil {
				t.Fatal(err)
			}

			dec := NewDecoder(r)
			for attempt := 1; attempt <= 2; attempt++ {
				var got map[string]int
				if err := dec.Decode(&got); err != nil {
					t.Fatalf("Decode %d: %v", attempt, err)
				}
				if dec.Format != format || !reflect.DeepEqual(got, want) {
					t.Fatalf("Decode %d: decoded %s %v, want %s %v", attempt, FormatNames[dec.Format], got, FormatNames[format], want)
				}
			}
		})
	}
}
