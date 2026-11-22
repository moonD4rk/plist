package plist

import (
	"sort"
	"strconv"
	"time"
)

// magic value used in the non-binary encoding of UIDs
// (stored as a dictionary mapping CF$UID->integer)
const cfUIDMagic = "CF$UID"

type cfValue interface {
	typeName() string
}

type cfDict struct {
	keys   sort.StringSlice
	values []cfValue
}

func (*cfDict) typeName() string {
	return "dictionary"
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

type cfString string

func (cfString) typeName() string {
	return "string"
}

type cfInteger struct {
	signed bool
	value  uint64
}

func (*cfInteger) typeName() string {
	return "integer"
}

type cfReal struct {
	is32  bool
	value float64
}

func (cfReal) typeName() string {
	return "real"
}

type cfBool bool

func (cfBool) typeName() string {
	return "boolean"
}

type cfUID UID

func (cfUID) typeName() string {
	return "UID"
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

type cfDate time.Time

func (cfDate) typeName() string {
	return "date"
}
