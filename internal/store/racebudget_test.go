//go:build !race

package store

import "time"

// resetBudget is the ceiling from the definition of done: tests call reset
// between cases, so it has to stay out of the way.
const resetBudget = 100 * time.Millisecond
