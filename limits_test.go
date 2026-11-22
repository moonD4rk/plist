package plist

import (
	"runtime"
	"strings"
	"testing"
)

// These tests exercise the runzero hardening:
// deeply nested plist input must return a parse error BEFORE the recursive
// parsers overflow the goroutine stack. A Go stack overflow is a fatal,
// unrecoverable runtime error (recover() cannot catch it), so the assertion is
// "Decode returns an error" rather than "Decode panics and we recover."
//
// The nesting count is chosen far above maxParseDepth but well below the depth
// that would overflow the stack, so if the cap were absent the test would still
// pass without crashing; we therefore also assert the returned error reports the
// depth limit, confirming the guard (not some incidental limit) fired.

func TestXMLPlistDepthLimit(t *testing.T) {
	const n = maxParseDepth + 50
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0">`)
	for i := 0; i < n; i++ {
		b.WriteString("<array>")
	}
	b.WriteString("<string>x</string>")
	for i := 0; i < n; i++ {
		b.WriteString("</array>")
	}
	b.WriteString("</plist>")

	var out any
	_, err := Unmarshal([]byte(b.String()), &out)
	if err == nil {
		t.Fatal("expected a parse error for deeply nested XML plist, got nil")
	}
	if !strings.Contains(err.Error(), errMaxDepthExceeded.Error()) {
		t.Fatalf("expected max-depth error, got: %v", err)
	}
}

func TestTextPlistDepthLimit(t *testing.T) {
	const n = maxParseDepth + 50
	// OpenStep nested dictionaries: {a={a={a=...;};};}
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("{a=")
	}
	b.WriteString("x")
	for i := 0; i < n; i++ {
		b.WriteString(";}")
	}

	var out any
	_, err := Unmarshal([]byte(b.String()), &out)
	if err == nil {
		t.Fatal("expected a parse error for deeply nested text plist, got nil")
	}
	if !strings.Contains(err.Error(), errMaxDepthExceeded.Error()) {
		t.Fatalf("expected max-depth error, got: %v", err)
	}
}

// buildDeepBplist constructs a binary plist whose top object is a chain of
// `depth` single-element arrays terminating in an ASCII string. Each array
// references the next via the object table, so the file is small but forces the
// binary parser to recurse `depth` levels.
func buildDeepBplist(depth int) []byte {
	// Object IDs: 0..depth-1 are arrays (0 = top, references 1, ... ), object
	// `depth` is the terminal ASCII string "x".
	numObjects := depth + 1
	// 2-byte offsets and 2-byte object refs so the file can exceed 255 bytes.
	header := []byte("bplist00")
	body := append([]byte(nil), header...)

	offsets := make([]int, numObjects)
	// Arrays: tag 0xA1 (array, count 1) followed by a 2-byte object ref.
	for i := 0; i < depth; i++ {
		offsets[i] = len(body)
		body = append(body, 0xA1, byte((i+1)>>8), byte(i+1))
	}
	// Terminal string "x": tag 0x51 (ASCII string, length 1) + 'x'.
	offsets[depth] = len(body)
	body = append(body, 0x51, 'x')

	// Offset table: 2 bytes per object (big-endian).
	offsetTableOffset := len(body)
	for _, off := range offsets {
		body = append(body, byte(off>>8), byte(off))
	}

	// Trailer (32 bytes): 5 unused, sortVersion, offsetIntSize, objectRefSize,
	// numObjects(8), topObject(8), offsetTableOffset(8).
	trailer := make([]byte, 32)
	trailer[6] = 2 // OffsetIntSize
	trailer[7] = 2 // ObjectRefSize
	putUint64BE(trailer[8:16], uint64(numObjects))
	putUint64BE(trailer[16:24], 0) // top object = 0
	putUint64BE(trailer[24:32], uint64(offsetTableOffset))
	body = append(body, trailer...)
	return body
}

func putUint64BE(b []byte, v uint64) {
	for i := 7; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
}

func TestBplistDepthLimit(t *testing.T) {
	// Keep depth small enough that all object offsets fit in one byte (<256)
	// but above maxParseDepth so the depth guard fires.
	depth := maxParseDepth + 10
	data := buildDeepBplist(depth)

	var out any
	_, err := Unmarshal(data, &out)
	if err == nil {
		t.Fatal("expected a parse error for deeply nested binary plist, got nil")
	}
	if !strings.Contains(err.Error(), errMaxDepthExceeded.Error()) {
		t.Fatalf("expected max-depth error, got: %v", err)
	}
}

// TestBplistTruncatedOffset confirms the bounds checks convert a crafted
// out-of-range read into a recoverable parse error rather than an out-of-bounds
// slice panic that would escape Decode.
func TestBplistShallowStillParses(t *testing.T) {
	// A small valid bplist (single array of one string) must still decode after
	// the hardening, guarding against an over-aggressive bounds/depth check.
	data := buildDeepBplist(2)
	var out any
	if _, err := Unmarshal(data, &out); err != nil {
		t.Fatalf("valid shallow bplist failed to decode: %v", err)
	}
}

func bplistCount(tag byte, n int) []byte {
	if n < 0xF {
		return []byte{tag | byte(n)}
	}
	return []byte{tag | 0xF, markerInt | 0x2, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
}

func bplistArray(refs ...int) []byte {
	b := bplistCount(markerArray, len(refs))
	for _, ref := range refs {
		b = append(b, byte(ref>>8), byte(ref))
	}
	return b
}

var bplistLeaf = []byte{markerASCII | 1, 'x'}

func repeatRef(ref, n int) []int {
	refs := make([]int, n)
	for i := range refs {
		refs[i] = ref
	}
	return refs
}

// ids[i] is the index in objects that object ID i points to, so several IDs can share one encoding; ID 0 is the root.
func assembleBplist(objects [][]byte, ids []int) []byte {
	doc := []byte("bplist00")
	offsets := make([]int, len(objects))
	for i, object := range objects {
		offsets[i] = len(doc)
		doc = append(doc, object...)
	}

	tableOffset := len(doc)
	for _, id := range ids {
		off := offsets[id]
		doc = append(doc, byte(off>>24), byte(off>>16), byte(off>>8), byte(off))
	}

	trailer := make([]byte, 32)
	trailer[6] = 4
	trailer[7] = 2
	putUint64BE(trailer[8:16], uint64(len(ids)))
	putUint64BE(trailer[24:32], uint64(tableOffset))
	return append(doc, trailer...)
}

func sequentialIDs(n int) []int {
	ids := make([]int, n)
	for i := range ids {
		ids[i] = i
	}
	return ids
}

// doublingBplist nests levels arrays, each holding two references to the next one.
func doublingBplist(levels int) []byte {
	objects := make([][]byte, 0, levels+1)
	for i := 1; i <= levels; i++ {
		objects = append(objects, bplistArray(i, i))
	}
	objects = append(objects, bplistLeaf)
	return assembleBplist(objects, sequentialIDs(len(objects)))
}

// stagedChainBplist nests 300 arrays but lists the deepest third first, so no single descent passes 101 levels.
func stagedChainBplist() []byte {
	const links = 300
	objects := [][]byte{bplistArray(1+200, 1+100, 1)}
	for i := 0; i < links; i++ {
		objects = append(objects, bplistArray(i+2))
	}
	objects = append(objects, bplistLeaf)
	return assembleBplist(objects, sequentialIDs(len(objects)))
}

// overlappingBplist points count object IDs at the bytes of one array of count references.
func overlappingBplist(count int) []byte {
	leaf := count + 1
	objects := [][]byte{nil, bplistArray(repeatRef(leaf, count)...), bplistLeaf}
	ids := []int{0}
	refs := make([]int, count)
	for i := range refs {
		refs[i] = i + 1
		ids = append(ids, 1)
	}
	objects[0] = bplistArray(refs...)
	return assembleBplist(objects, append(ids, 2))
}

// aliasedBplist points count object IDs at the bytes of one string or data object.
func aliasedBplist(tag byte, units, unitSize, count int) []byte {
	object := append(bplistCount(tag, units), make([]byte, units*unitSize)...)
	ids := []int{0}
	refs := make([]int, count)
	for i := range refs {
		refs[i] = i + 1
		ids = append(ids, 1)
	}
	return assembleBplist([][]byte{bplistArray(refs...), object}, ids)
}

// sharedArrayBplist refers to one array of size strings from a root array, references times.
func sharedArrayBplist(references, size int) []byte {
	objects := [][]byte{
		bplistArray(repeatRef(1, references)...),
		bplistArray(repeatRef(2, size)...),
		bplistLeaf,
	}
	return assembleBplist(objects, sequentialIDs(len(objects)))
}

func TestBplistValueBudget(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{"two references per level", doublingBplist(21), errTooManyValues},
		{"object IDs sharing one encoding", overlappingBplist(2000), errTooManyValues},
		{"object IDs sharing one data object", aliasedBplist(markerData, 4096, 1, 2000), errTooManyValues},
		{"object IDs sharing one ASCII string", aliasedBplist(markerASCII, 4096, 1, 2000), errTooManyValues},
		{"object IDs sharing one UTF-16 string", aliasedBplist(markerUTF16, 2048, 2, 2000), errTooManyValues},
		{"shared array over the budget", sharedArrayBplist(6000, 100), errTooManyValues},
		{"depth reached through parsed objects", stagedChainBplist(), errMaxDepthExceeded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out any
			_, err := Unmarshal(tc.data, &out)
			if err == nil {
				t.Fatalf("Unmarshal of %d bytes succeeded, want %q", len(tc.data), tc.want)
			}
			if !strings.Contains(err.Error(), tc.want.Error()) {
				t.Fatalf("Unmarshal error = %q, want %q", err, tc.want)
			}
		})
	}
}

func TestBplistSharedContainersWithinBudget(t *testing.T) {
	const references, size = 5000, 100

	var out [][]string
	if _, err := Unmarshal(sharedArrayBplist(references, size), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != references || len(out[references-1]) != size {
		t.Fatalf("decoded %d arrays, the last of %d strings; want %d arrays of %d", len(out), len(out[len(out)-1]), references, size)
	}
}

func TestBplistValueBudgetGrowsWithDocument(t *testing.T) {
	const elements = minValueBudget + 1000

	data, err := Marshal(make([]int, elements), BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	if _, err := Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != elements {
		t.Fatalf("decoded %d elements, want %d", len(out), elements)
	}

	large := struct {
		Data  []byte
		ASCII string
		UTF16 string
	}{make([]byte, elements), strings.Repeat("a", elements), strings.Repeat("é", elements)}
	if data, err = Marshal(large, BinaryFormat); err != nil {
		t.Fatal(err)
	}
	back := large
	back.Data, back.ASCII, back.UTF16 = nil, "", ""
	if _, err := Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Data) != elements || back.ASCII != large.ASCII || back.UTF16 != large.UTF16 {
		t.Fatalf("decoded %d bytes of data and strings of %d and %d bytes", len(back.Data), len(back.ASCII), len(back.UTF16))
	}
}

func TestBplistSharedDataWithinBudget(t *testing.T) {
	const references = 1000
	blob := make([]byte, 100000)

	shared := make([][]byte, references)
	for i := range shared {
		shared[i] = blob
	}
	data, err := Marshal(shared, BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 2*len(blob) {
		t.Fatalf("the document is %d bytes, want the data stored once", len(data))
	}
	var out [][]byte
	if _, err := Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != references || len(out[references-1]) != len(blob) {
		t.Fatalf("decoded %d values, want %d of %d bytes", len(out), references, len(blob))
	}
}

func allocatedBytes(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestDecodeMemoryIsProportionalToInput(t *testing.T) {
	const repeat = 20000
	const limit = 96 // allocated bytes per input byte

	tests := []struct {
		name     string
		document string
	}{
		{"text dictionaries", "(" + strings.Repeat("{},", repeat) + ")"},
		{"text arrays", "(" + strings.Repeat("(),", repeat) + ")"},
		{"text data", "(" + strings.Repeat("<>,", repeat) + ")"},
		{"text escapes", `"` + strings.Repeat(`a\n`, 5*repeat) + `"`},
		{"XML dictionaries", "<plist><array>" + strings.Repeat("<dict/>", repeat) + "</array></plist>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(tc.document)
			var out any
			var err error
			allocated := allocatedBytes(func() { _, err = Unmarshal(data, &out) })
			if err != nil {
				t.Fatal(err)
			}
			perByte := allocated / uint64(len(data))
			t.Logf("%d bytes allocated for %d bytes of input (%d per byte)", allocated, len(data), perByte)
			if perByte > limit {
				t.Errorf("allocated %d bytes per input byte, want at most %d", perByte, limit)
			}
		})
	}
}
