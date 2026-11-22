//go:build gofuzz

package plist

import (
	"bytes"
)

// Fuzz is an entry point for go-fuzz. It is built only with the gofuzz build tag.
func Fuzz(data []byte) int {
	buf := bytes.NewReader(data)

	var obj any
	if err := NewDecoder(buf).Decode(&obj); err != nil {
		return 0
	}
	return 1
}
