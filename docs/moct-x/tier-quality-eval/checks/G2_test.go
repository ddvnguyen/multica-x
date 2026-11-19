package claim

import "testing"

func hosts() map[string][]string {
	return map[string][]string{
		"tier-fast":  {"rt-a", "rt-b"},
		"tier-cheap": {"rt-b"},
		"tier-gone":  {},
	}
}

func TestClaimableLegacySameRuntime(t *testing.T) {
	task := Task{ID: "k1", RuntimeID: "rt-a"}
	if !Claimable(task, "rt-a", hosts()) {
		t.Fatal("legacy task must be claimable by its own runtime")
	}
}

func TestClaimableLegacyRejectsOtherRuntime(t *testing.T) {
	task := Task{ID: "k1", RuntimeID: "rt-a"}
	if Claimable(task, "rt-b", hosts()) {
		t.Fatal("legacy task must not be claimable by another runtime")
	}
}

func TestClaimableStampedAcrossRuntimes(t *testing.T) {
	task := Task{ID: "k2", RuntimeID: "rt-a", TierID: "tier-fast"}
	if !Claimable(task, "rt-b", hosts()) {
		t.Fatal("stamped task must be claimable on a runtime hosting its tier")
	}
	if Claimable(task, "rt-c", hosts()) {
		t.Fatal("stamped task must not be claimable on a runtime that does not host its tier")
	}
}

func TestClaimableStampedUnknownTier(t *testing.T) {
	task := Task{ID: "k3", RuntimeID: "rt-b", TierID: "tier-nope"}
	if Claimable(task, "rt-b", hosts()) {
		t.Fatal("stamped task with unknown tier must be refused, not fall back to legacy")
	}
	task = Task{ID: "k4", TierID: "tier-gone"}
	if Claimable(task, "rt-b", hosts()) {
		t.Fatal("tier with no hosts must not be claimable")
	}
}

func TestClaimableRejectsEmptyRuntime(t *testing.T) {
	if Claimable(Task{ID: "k5", RuntimeID: ""}, "", hosts()) {
		t.Fatal("empty runtime must never claim")
	}
}
