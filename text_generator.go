package plist

import (
	"encoding/hex"
	"io"
	"strconv"
	"strings"
	"time"
)

type textPlistGenerator struct {
	writer io.Writer
	format int

	quotableTable *characterSet

	indent string
	depth  int

	dictKvDelimiter, dictEntryDelimiter, arrayDelimiter []byte
}

var (
	textPlistTimeLayout = "2006-01-02 15:04:05 -0700"
	padding             = "0000"
)

func (p *textPlistGenerator) generateDocument(pval cfValue) {
	p.writePlistValue(pval)
}

func (p *textPlistGenerator) plistQuotedString(str string) string {
	if str == "" {
		return `""`
	}
	var b strings.Builder
	b.Grow(len(str))
	// Unquoted, either prefix would start a comment.
	quot := strings.HasPrefix(str, "//") || strings.HasPrefix(str, "/*")
	for _, r := range str {
		switch {
		case r > 0xFF:
			quot = true
			b.WriteString(`\U`)
			us := strconv.FormatInt(int64(r), 16)
			// Pad to >=4 hex digits without slicing a fixed-width string (the old
			// padding[len(us):] panicked on astral runes >= U+10000). NB: a single
			// 5-6 digit \U is not a faithful OpenStep/GNUStep escape — that format
			// encodes UTF-16 code units, so astral runes need a surrogate pair, and
			// this package's text parser reads only 4 hex digits. Such runes do NOT
			// round-trip through OpenStep/GNUStep (they do through XML/Binary); this
			// branch only guarantees Marshal no longer panics. See TestAstralRuneMarshal.
			for i := len(us); i < 4; i++ {
				b.WriteByte('0')
			}
			b.WriteString(us)
		case r > 0x7F:
			quot = true
			b.WriteByte('\\')
			us := strconv.FormatInt(int64(r), 8)
			b.WriteString(padding[1+len(us):])
			b.WriteString(us)
		default:
			c := uint8(r)
			if p.quotableTable.ContainsByte(c) {
				quot = true
			}

			switch c {
			case '\a':
				b.WriteString(`\a`)
			case '\b':
				b.WriteString(`\b`)
			case '\v':
				b.WriteString(`\v`)
			case '\f':
				b.WriteString(`\f`)
			case '\\':
				b.WriteString(`\\`)
			case '"':
				b.WriteString(`\"`)
			case '\t', '\r', '\n':
				fallthrough
			default:
				b.WriteByte(c)
			}
		}
	}
	if quot {
		return `"` + b.String() + `"`
	}
	return b.String()
}

func (p *textPlistGenerator) deltaIndent(depthDelta int) {
	if depthDelta < 0 {
		p.depth--
	} else if depthDelta > 0 {
		p.depth++
	}
}

func (p *textPlistGenerator) writeIndent() {
	if len(p.indent) == 0 {
		return
	}
	if len(p.indent) > 0 {
		p.writer.Write([]byte("\n"))
		for i := 0; i < p.depth; i++ {
			io.WriteString(p.writer, p.indent)
		}
	}
}

func (p *textPlistGenerator) writePlistValue(pval cfValue) {
	if pval == nil {
		return
	}

	switch pval := pval.(type) {
	case *cfDictionary:
		pval.sort()
		p.writer.Write([]byte(`{`))
		p.deltaIndent(1)
		for i, k := range pval.keys {
			p.writeIndent()
			io.WriteString(p.writer, p.plistQuotedString(k))
			p.writer.Write(p.dictKvDelimiter)
			p.writePlistValue(pval.values[i])
			p.writer.Write(p.dictEntryDelimiter)
		}
		p.deltaIndent(-1)
		p.writeIndent()
		p.writer.Write([]byte(`}`))
	case *cfArray:
		p.writer.Write([]byte(`(`))
		p.deltaIndent(1)
		for _, v := range pval.values {
			p.writeIndent()
			p.writePlistValue(v)
			p.writer.Write(p.arrayDelimiter)
		}
		p.deltaIndent(-1)
		p.writeIndent()
		p.writer.Write([]byte(`)`))
	case cfString:
		io.WriteString(p.writer, p.plistQuotedString(string(pval)))
	case *cfNumber:
		if p.format == GNUStepFormat {
			p.writer.Write([]byte(`<*I`))
		}
		if pval.signed {
			io.WriteString(p.writer, strconv.FormatInt(int64(pval.value), 10))
		} else {
			io.WriteString(p.writer, strconv.FormatUint(pval.value, 10))
		}
		if p.format == GNUStepFormat {
			p.writer.Write([]byte(`>`))
		}
	case *cfReal:
		// GNUstep does not differentiate between 32/64-bit floats.
		num := strconv.FormatFloat(pval.value, 'g', -1, 64)
		switch {
		case p.format == GNUStepFormat:
			io.WriteString(p.writer, `<*R`+num+`>`)
		case strings.Contains(num, "+"):
			// CoreFoundation does not read + in an unquoted string.
			io.WriteString(p.writer, `"`+num+`"`)
		default:
			io.WriteString(p.writer, num)
		}
	case cfBoolean:
		if p.format == GNUStepFormat {
			if pval {
				p.writer.Write([]byte(`<*BY>`))
			} else {
				p.writer.Write([]byte(`<*BN>`))
			}
		} else {
			if pval {
				p.writer.Write([]byte(`1`))
			} else {
				p.writer.Write([]byte(`0`))
			}
		}
	case cfData:
		var hexencoded [9]byte
		var l int
		asc := 9
		hexencoded[8] = ' '

		p.writer.Write([]byte(`<`))
		b := []byte(pval)
		for i := 0; i < len(b); i += 4 {
			l = i + 4
			if l >= len(b) {
				l = len(b)
				// We no longer need the space - or the rest of the buffer.
				// (we used >= above to get this part without another conditional :P)
				asc = (l - i) * 2
			}
			// Fill the buffer (only up to 8 characters, to preserve the space we implicitly include
			// at the end of every encode)
			hex.Encode(hexencoded[:8], b[i:l])
			p.writer.Write(hexencoded[:asc])
		}
		p.writer.Write([]byte(`>`))
	case cfDate:
		if p.format == GNUStepFormat {
			p.writer.Write([]byte(`<*D`))
			io.WriteString(p.writer, time.Time(pval).In(time.UTC).Format(textPlistTimeLayout))
			p.writer.Write([]byte(`>`))
		} else {
			io.WriteString(p.writer, p.plistQuotedString(time.Time(pval).In(time.UTC).Format(textPlistTimeLayout)))
		}
	case cfUID:
		p.writePlistValue(pval.toDict())
	}
}

func (p *textPlistGenerator) Indent(i string) {
	p.indent = i
	if i == "" {
		p.dictKvDelimiter = []byte(`=`)
	} else {
		// For pretty-printing
		p.dictKvDelimiter = []byte(` = `)
	}
}

func newTextPlistGenerator(w io.Writer, format int) *textPlistGenerator {
	table := &osQuotable
	if format == GNUStepFormat {
		table = &gsQuotable
	}
	return &textPlistGenerator{
		writer:             mustWriter{w},
		format:             format,
		quotableTable:      table,
		dictKvDelimiter:    []byte(`=`),
		arrayDelimiter:     []byte(`,`),
		dictEntryDelimiter: []byte(`;`),
	}
}
