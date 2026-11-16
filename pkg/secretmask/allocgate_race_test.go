//go:build race

package secretmask

// allocGateInstrumented is true under -race: race instrumentation adds
// runtime allocations, so allocation ceilings are only meaningful without it.
// The gates still run in the normal CI test pass.
const allocGateInstrumented = true
