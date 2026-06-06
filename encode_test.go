package plist

import (
	"bytes"
	"fmt"
	"testing"
)

func TestEncode(t *testing.T) {
	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			for fmt, doc := range test.Documents {
				if test.SkipEncode[fmt] {
					continue
				}
				t.Run(FormatNames[fmt], func(t *testing.T) {
					encoded, err := Marshal(test.Value, fmt)
					if err != nil {
						t.Error(err)
					}

					if !bytes.Equal(doc, encoded) {
						printype := "%s"
						if fmt == BinaryFormat {
							printype = "%2x"
						}
						t.Logf("Value: %#v", test.Value)
						t.Logf("Expected: "+printype+"\n", doc)
						t.Logf("Received: "+printype+"\n", encoded)
						t.Fail()
					}
				})
			}
		})
	}
}

func ExampleEncoder_Encode() {
	type sparseBundleHeader struct {
		InfoDictionaryVersion string `plist:"CFBundleInfoDictionaryVersion"`
		BandSize              uint64 `plist:"band-size"`
		BackingStoreVersion   int    `plist:"bundle-backingstore-version"`
		DiskImageBundleType   string `plist:"diskimage-bundle-type"`
		Size                  uint64 `plist:"size"`
	}
	data := &sparseBundleHeader{
		InfoDictionaryVersion: "6.0",
		BandSize:              8388608,
		Size:                  4 * 1048576 * 1024 * 1024,
		DiskImageBundleType:   "com.apple.diskimage.sparsebundle",
		BackingStoreVersion:   1,
	}

	buf := &bytes.Buffer{}
	encoder := NewEncoder(buf)
	err := encoder.Encode(data)
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println(buf.String())

	// Output: <?xml version="1.0" encoding="UTF-8"?>
	// <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
	// <plist version="1.0"><dict><key>CFBundleInfoDictionaryVersion</key><string>6.0</string><key>band-size</key><integer>8388608</integer><key>bundle-backingstore-version</key><integer>1</integer><key>diskimage-bundle-type</key><string>com.apple.diskimage.sparsebundle</string><key>size</key><integer>4398046511104</integer></dict></plist>
}

func ExampleMarshal_xml() {
	type sparseBundleHeader struct {
		InfoDictionaryVersion string `plist:"CFBundleInfoDictionaryVersion"`
		BandSize              uint64 `plist:"band-size"`
		BackingStoreVersion   int    `plist:"bundle-backingstore-version"`
		DiskImageBundleType   string `plist:"diskimage-bundle-type"`
		Size                  uint64 `plist:"size"`
	}
	data := &sparseBundleHeader{
		InfoDictionaryVersion: "6.0",
		BandSize:              8388608,
		Size:                  4 * 1048576 * 1024 * 1024,
		DiskImageBundleType:   "com.apple.diskimage.sparsebundle",
		BackingStoreVersion:   1,
	}

	plist, err := MarshalIndent(data, XMLFormat, "\t")
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println(string(plist))

	// Output: <?xml version="1.0" encoding="UTF-8"?>
	// <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
	// <plist version="1.0">
	// 	<dict>
	// 		<key>CFBundleInfoDictionaryVersion</key>
	// 		<string>6.0</string>
	// 		<key>band-size</key>
	// 		<integer>8388608</integer>
	// 		<key>bundle-backingstore-version</key>
	// 		<integer>1</integer>
	// 		<key>diskimage-bundle-type</key>
	// 		<string>com.apple.diskimage.sparsebundle</string>
	// 		<key>size</key>
	// 		<integer>4398046511104</integer>
	// 	</dict>
	// </plist>
}

func ExampleMarshal_gnustep() {
	type sparseBundleHeader struct {
		InfoDictionaryVersion string `plist:"CFBundleInfoDictionaryVersion"`
		BandSize              uint64 `plist:"band-size"`
		BackingStoreVersion   int    `plist:"bundle-backingstore-version"`
		DiskImageBundleType   string `plist:"diskimage-bundle-type"`
		Size                  uint64 `plist:"size"`
	}
	data := &sparseBundleHeader{
		InfoDictionaryVersion: "6.0",
		BandSize:              8388608,
		Size:                  4 * 1048576 * 1024 * 1024,
		DiskImageBundleType:   "com.apple.diskimage.sparsebundle",
		BackingStoreVersion:   1,
	}

	plist, err := MarshalIndent(data, GNUStepFormat, "\t")
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println(string(plist))

	// Output: {
	// 	CFBundleInfoDictionaryVersion = 6.0;
	// 	band-size = <*I8388608>;
	// 	bundle-backingstore-version = <*I1>;
	// 	diskimage-bundle-type = com.apple.diskimage.sparsebundle;
	// 	size = <*I4398046511104>;
	// }
}

// An out-of-range encoding format must return an error, not dereference a nil
// generator and re-panic past Encode's recover.
func TestInvalidEncodeFormatReturnsError(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Marshal with invalid format panicked instead of returning error: %v", r)
		}
	}()
	if _, err := Marshal(map[string]any{"a": 1}, 99); err == nil {
		t.Fatal("expected error for invalid format, got nil")
	}
}

// TestAstralRuneMarshal pins the full contract for a string with an astral-plane
// rune (>= U+10000, e.g. an emoji): no generator may crash. XML and Binary
// round-trip it faithfully, but OpenStep/GNUStep do NOT — that old text format
// encodes UTF-16 code units (an astral rune is a surrogate pair of two 4-digit
// \U escapes) and this package's text parser reads only 4 hex digits, so the
// rune is lossy there. If the OpenStep/GNUStep assertions below ever start
// failing because the value now round-trips, someone implemented surrogate
// pairs — update this test to assert faithful round-trip instead.
func TestAstralRuneMarshal(t *testing.T) {
	const value = "grin \U0001F600 end"

	// XML and Binary are faithful.
	for _, f := range []int{XMLFormat, BinaryFormat} {
		f := f
		t.Run(FormatNames[f], func(t *testing.T) {
			b, err := Marshal(map[string]string{"e": value}, f)
			if err != nil {
				t.Fatalf("Marshal(%s): %v", FormatNames[f], err)
			}
			var out map[string]string
			if _, err := Unmarshal(b, &out); err != nil {
				t.Fatalf("Unmarshal(%s): %v", FormatNames[f], err)
			}
			if out["e"] != value {
				t.Fatalf("%s round-trip mismatch: got %q want %q", FormatNames[f], out["e"], value)
			}
		})
	}

	// OpenStep/GNUStep must not panic, but astral runes are knowingly lossy.
	for _, f := range []int{OpenStepFormat, GNUStepFormat} {
		f := f
		t.Run(FormatNames[f], func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Marshal(%s) panicked on astral rune: %v", FormatNames[f], r)
				}
			}()
			b, err := Marshal(map[string]string{"e": value}, f)
			if err != nil {
				t.Fatalf("Marshal(%s): %v", FormatNames[f], err)
			}
			var out map[string]string
			if _, err := Unmarshal(b, &out); err != nil {
				t.Fatalf("Unmarshal(%s): %v", FormatNames[f], err)
			}
			if out["e"] == value {
				t.Fatalf("%s now round-trips astral runes faithfully — update this test to assert it", FormatNames[f])
			}
			t.Logf("%s astral round-trip is lossy (documented limitation): %q -> %q", FormatNames[f], value, out["e"])
		})
	}
}
