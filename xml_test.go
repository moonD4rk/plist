package plist

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

var InvalidXMLPlists = []struct {
	Name string
	Data string
}{
	{"hex integer with no digits", `<plist version="1.0"><integer>0x</integer></plist>`},
	{"unknown element doct", "<plist><doct><key>helo</key><string></string></doct></plist>"},
	{"dict with string instead of key", "<plist><dict><string>helo</string></dict></plist>"},
	{"dict with key but no value", "<plist><dict><key>helo</key></dict></plist>"},
	{"integer with non-numeric value", "<plist><integer>helo</integer></plist>"},
	{"empty integer", "<plist><integer></integer></plist>"},
	{"real with non-numeric value", "<plist><real>helo</real></plist>"},
	{"data with invalid base64", "<plist><data>*@&amp;%#helo</data></plist>"},
	{"date with invalid format", "<plist><date>*@&amp;%#helo</date></plist>"},
	{"unclosed integer tag", "<plist><integer>10</plist>"},
	{"unclosed real tag", "<plist><real>10</plist>"},
	{"unclosed string tag", "<plist><string>10</plist>"},
	{"unclosed dict tag", "<plist><dict>10</plist>"},
	{"unclosed key tag", "<plist><dict><key>10</plist>"},
	{"truncated plist open tag", "<plist>"},
	{"truncated data tag", "<plist><data>"},
	{"truncated date tag", "<plist><date>"},
	{"truncated array tag", "<plist><array>"},
	{"self-closing empty plist", "<plist/>"},
	{"truncated XML", "<pl"},
	{"binary plist magic as XML", "bplist00"},
}

func TestVariousIllegalXMLPlists(t *testing.T) {
	for i, tc := range InvalidXMLPlists {
		t.Run(fmt.Sprintf("%d_%s", i, tc.Name), func(t *testing.T) {
			buf := bytes.NewReader([]byte(tc.Data))
			d := newXMLPlistParser(buf)
			_, err := d.parseDocument()
			if err == nil {
				t.Errorf("expected error for invalid plist %q, got nil", tc.Data)
			} else {
				t.Logf("Error: %v", err)
			}
		})
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestXMLEncoderReturnsWriteError(t *testing.T) {
	want := errors.New("disk full")
	if err := NewEncoder(failingWriter{want}).Encode("short document"); !errors.Is(err, want) {
		t.Errorf("Encode error = %v, want %v", err, want)
	}
}

func TestXMLEmptyData(t *testing.T) {
	type record struct {
		Empty []byte
		Nil   []byte
	}
	data, err := Marshal(record{Empty: []byte{}}, XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	want := xmlPreamble + `<plist version="1.0"><dict><key>Empty</key><data></data><key>Nil</key><data></data></dict></plist>`
	if string(data) != want {
		t.Errorf("encoded %s\nwant    %s", data, want)
	}

	indented, err := MarshalIndent([][]byte{{}}, XMLFormat, "\t")
	if err != nil {
		t.Fatal(err)
	}
	want = xmlPreamble + "<plist version=\"1.0\">\n\t<array>\n\t\t<data></data>\n\t</array>\n</plist>"
	if string(indented) != want {
		t.Errorf("encoded %s\nwant    %s", indented, want)
	}

	var back record
	if _, err := Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Empty == nil || len(back.Empty) != 0 || len(back.Nil) != 0 {
		t.Errorf("decoded %#v, want two empty values", back)
	}
}
