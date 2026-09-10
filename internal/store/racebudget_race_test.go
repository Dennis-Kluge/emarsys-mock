//go:build race

package store

import "time"

// The race detector multiplies the cost of every memory access, so timing
// anything under it measures the instrumentation rather than the code. The real
// budget lives in the non-race build of this file; this one only guards against
// a reset that has become pathologically slow.
const resetBudget = 2 * time.Second
