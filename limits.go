package plist

import "errors"

// maxParseDepth bounds container (dict/array) nesting in all plist parsers
// (XML, OpenStep/GNUStep text, and binary). Without it, a hostile plist with
// deeply nested containers drives the parser into unbounded recursion until the
// goroutine stack exceeds Go's limit, which is a fatal, unrecoverable runtime
// error (recover() cannot catch a stack overflow). Returning a parse error well
// before that point lets callers treat the artifact as "skip" rather than
// crashing the process. The limit is generous relative to any real-world plist.
//
// runzero patch.
const maxParseDepth = 128

// errMaxDepthExceeded is returned (via panic, matching the package's existing
// error-handling convention) when nesting exceeds maxParseDepth.
var errMaxDepthExceeded = errors.New("plist: maximum nesting depth exceeded")

const minValueBudget = 1 << 19

var errTooManyValues = errors.New("plist: document expands to too many values")

// A binary plist that shares no containers holds at most one value per byte, so it never reaches the budget.
func valueBudget(size int) uint64 {
	if size > minValueBudget {
		return uint64(size)
	}
	return minValueBudget
}
