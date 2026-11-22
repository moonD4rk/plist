package plist

import (
	"math"
	"sort"
	"strconv"
	"time"
)

// magic value used in the non-binary encoding of UIDs
// (stored as a dictionary mapping CF$UID->integer)
const cfUIDMagic = "CF$UID"

type cfValue interface {
	typeName() string
	hash() any
}

type cfDict struct {
	keys   sort.StringSlice
	values []cfValue
}

func (*cfDict) typeName() string {
	return "dictionary"
}

func (p *cfDict) hash() any {
	return p
}

func (p *cfDict) Len() int {
	return len(p.keys)
}

func (p *cfDict) Less(i, j int) bool {
	return p.keys.Less(i, j)
}

func (p *cfDict) Swap(i, j int) {
	p.keys.Swap(i, j)
	p.values[i], p.values[j] = p.values[j], p.values[i]
}

func (p *cfDict) sort() {
	sort.Sort(p)
}

func (p *cfDict) maybeUID(lax bool) cfValue {
	if len(p.keys) == 1 && p.keys[0] == "CF$UID" && len(p.values) == 1 {
		pval := p.values[0]
		if integer, ok := pval.(*cfInteger); ok {
			return cfUID(integer.value)
		}
		// Openstep only has cfString. Act like the unmarshaller a bit.
		if lax {
			if str, ok := pval.(cfString); ok {
				if i, err := strconv.ParseUint(string(str), 10, 64); err == nil {
					return cfUID(i)
				}
			}
		}
	}
	return p
}

type cfArray struct {
	values []cfValue
}

func (*cfArray) typeName() string {
	return "array"
}

func (p *cfArray) hash() any {
	return p
}

type cfString string

func (cfString) typeName() string {
	return "string"
}

func (p cfString) hash() any {
	return string(p)
}

type cfInteger struct {
	signed bool
	value  uint64
}

func (*cfInteger) typeName() string {
	return "integer"
}

func (p *cfInteger) hash() any {
	if p.signed {
		return int64(p.value)
	}
	return p.value
}

type cfReal struct {
	is32  bool
	value float64
}

func (cfReal) typeName() string {
	return "real"
}

// realKey compares bit patterns: as a map key NaN never equals itself and -0 equals +0.
type realKey struct {
	bits uint64
	is32 bool
}

func (p *cfReal) hash() any {
	if p.is32 {
		return realKey{bits: uint64(math.Float32bits(float32(p.value))), is32: true}
	}
	return realKey{bits: math.Float64bits(p.value)}
}

type cfBool bool

func (cfBool) typeName() string {
	return "boolean"
}

func (p cfBool) hash() any {
	return bool(p)
}

type cfUID UID

func (cfUID) typeName() string {
	return "UID"
}

func (p cfUID) hash() any {
	return p
}

func (p cfUID) toDict() *cfDict {
	return &cfDict{
		keys: []string{cfUIDMagic},
		values: []cfValue{&cfInteger{
			signed: false,
			value:  uint64(p),
		}},
	}
}

type cfData []byte

func (cfData) typeName() string {
	return "data"
}

// dataKey views the bytes as a string, so data are uniqued by their contents without being copied.
type dataKey string

func (p cfData) hash() any {
	return dataKey(zeroCopy8BitString(p, 0, len(p)))
}

type cfDate time.Time

func (cfDate) typeName() string {
	return "date"
}

func (p cfDate) hash() any {
	return time.Time(p)
}
