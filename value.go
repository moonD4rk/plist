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

func (d *cfDict) hash() any {
	return d
}

func (d *cfDict) Len() int {
	return len(d.keys)
}

func (d *cfDict) Less(i, j int) bool {
	return d.keys.Less(i, j)
}

func (d *cfDict) Swap(i, j int) {
	d.keys.Swap(i, j)
	d.values[i], d.values[j] = d.values[j], d.values[i]
}

func (d *cfDict) sort() {
	sort.Sort(d)
}

func (d *cfDict) maybeUID(coerceStrings bool) cfValue {
	if len(d.keys) == 1 && d.keys[0] == cfUIDMagic && len(d.values) == 1 {
		value := d.values[0]
		if integer, ok := value.(*cfInteger); ok {
			return cfUID(integer.value)
		}
		// Openstep only has cfString. Act like the unmarshaller a bit.
		if coerceStrings {
			if str, ok := value.(cfString); ok {
				if i, err := strconv.ParseUint(string(str), 10, 64); err == nil {
					return cfUID(i)
				}
			}
		}
	}
	return d
}

type cfArray struct {
	values []cfValue
}

func (*cfArray) typeName() string {
	return "array"
}

func (a *cfArray) hash() any {
	return a
}

type cfString string

func (cfString) typeName() string {
	return "string"
}

func (s cfString) hash() any {
	return string(s)
}

type cfInteger struct {
	signed bool
	value  uint64
}

func (*cfInteger) typeName() string {
	return "integer"
}

func (n *cfInteger) hash() any {
	if n.signed {
		return int64(n.value)
	}
	return n.value
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

func (r *cfReal) hash() any {
	if r.is32 {
		return realKey{bits: uint64(math.Float32bits(float32(r.value))), is32: true}
	}
	return realKey{bits: math.Float64bits(r.value)}
}

type cfBool bool

func (cfBool) typeName() string {
	return "boolean"
}

func (b cfBool) hash() any {
	return bool(b)
}

type cfUID UID

func (cfUID) typeName() string {
	return "UID"
}

func (u cfUID) hash() any {
	return u
}

func (u cfUID) toDict() *cfDict {
	return &cfDict{
		keys: []string{cfUIDMagic},
		values: []cfValue{&cfInteger{
			signed: false,
			value:  uint64(u),
		}},
	}
}

type cfData []byte

func (cfData) typeName() string {
	return "data"
}

// dataKey views the bytes as a string, so data are uniqued by their contents without being copied.
type dataKey string

func (d cfData) hash() any {
	return dataKey(zeroCopy8BitString(d, 0, len(d)))
}

type cfDate time.Time

func (cfDate) typeName() string {
	return "date"
}

func (d cfDate) hash() any {
	return time.Time(d)
}
