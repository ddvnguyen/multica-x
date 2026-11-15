package main

import (
	"testing"
	"time"

	// The server embeds tzdata (main.go); import it here too so the test is
	// hermetic on hosts that do not ship /usr/share/zoneinfo.
	_ "time/tzdata"
)

// M-335: the migrated multica_mmx copy carries Asia/Saigon cron schedules
// while the host image has no zoneinfo for it; the embedded tzdata must
// resolve the zone.
func TestLoadLocationAsiaSaigon(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Saigon")
	if err != nil {
		t.Fatalf("LoadLocation(Asia/Saigon): %v", err)
	}
	if got, want := loc.String(), "Asia/Saigon"; got != want {
		t.Fatalf("location = %q, want %q", got, want)
	}
	if _, offset := time.Now().In(loc).Zone(); offset != 7*3600 {
		t.Fatalf("Asia/Saigon offset = %d, want %d", offset, 7*3600)
	}
}
