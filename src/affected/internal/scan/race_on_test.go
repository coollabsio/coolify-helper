//go:build race

package scan

// raceEnabled skips the performance tests: the race detector makes them 10-20x slower.
const raceEnabled = true
