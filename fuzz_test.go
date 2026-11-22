package plist

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const fuzzMaxInputSize = 1 << 16

var allFormats = []int{XMLFormat, BinaryFormat, OpenStepFormat, GNUStepFormat}

type fuzzRecord struct {
	Name    string
	Count   int64
	Size    uint16
	Ratio   float64
	Enabled bool
	Payload []byte
	Created time.Time
	Ref     UID
	Tags    []string
	Scores  map[string]int
	Child   *fuzzRecord
	Extra   any
}

func fuzzDecodeTargets() []any {
	return []any{
		new(any),
		new(string),
		new(int64),
		new(uint8),
		new(float32),
		new(bool),
		new([]byte),
		new(time.Time),
		new(UID),
		new([]string),
		new([2]int),
		new(map[string]string),
		new(fuzzRecord),
		new([]fuzzRecord),
		new(map[string]fuzzRecord),
	}
}

func addDocumentSeeds(f *testing.F, format int) {
	for _, td := range tests {
		if doc, ok := td.Documents[format]; ok {
			f.Add(doc)
		}
	}
}

func fuzzUnmarshal(t *testing.T, data []byte) {
	if len(data) > fuzzMaxInputSize {
		t.Skip()
	}
	for _, target := range fuzzDecodeTargets() {
		_, _ = Unmarshal(data, target)
	}
}

func FuzzUnmarshalXML(f *testing.F) {
	addDocumentSeeds(f, XMLFormat)
	for _, tc := range InvalidXMLPlists {
		f.Add([]byte(tc.Data))
	}
	paths, err := filepath.Glob(filepath.Join("testdata", "xml_unusual_cases", "*.plist"))
	if err != nil {
		f.Fatal(err)
	}
	for _, path := range paths {
		doc, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(doc)
	}
	f.Fuzz(fuzzUnmarshal)
}

func FuzzUnmarshalBinary(f *testing.F) {
	addDocumentSeeds(f, BinaryFormat)
	for _, doc := range InvalidBplists {
		f.Add(doc)
	}
	f.Add(doublingBplist(21))
	f.Add(stagedChainBplist())
	f.Add(overlappingBplist(2000))
	f.Fuzz(fuzzUnmarshal)
}

func FuzzUnmarshalOpenStep(f *testing.F) {
	addDocumentSeeds(f, OpenStepFormat)
	for _, tc := range InvalidTextPlists {
		f.Add([]byte(tc.Data))
	}
	f.Fuzz(fuzzUnmarshal)
}

func FuzzUnmarshalGNUstep(f *testing.F) {
	addDocumentSeeds(f, GNUStepFormat)
	f.Fuzz(fuzzUnmarshal)
}

func FuzzReencode(f *testing.F) {
	for _, format := range allFormats {
		addDocumentSeeds(f, format)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzMaxInputSize {
			t.Skip()
		}
		var value any
		if _, err := Unmarshal(data, &value); err != nil {
			t.Skip()
		}
		for _, format := range allFormats {
			_, _ = Marshal(value, format)
		}
	})
}
