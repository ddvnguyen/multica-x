package m161redfail

import "testing"

// TestM161RedGate fails on purpose: the backend aggregate must go red.
func TestM161RedGate(t *testing.T) {
	t.Fatal("M-161 red-gate proof: deliberate failure")
}
