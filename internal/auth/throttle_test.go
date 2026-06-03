package auth

import (
	"testing"
	"time"
)

func TestThrottleAllowsBelowLimit(t *testing.T) {
	th := NewLoginThrottle(time.Minute, 3, time.Minute)
	for i := 0; i < 3; i++ {
		if ok, _ := th.Allow("k"); !ok {
			t.Fatalf("attempt %d should be allowed", i)
		}
		th.RecordFailure("k")
	}
	// Fourth attempt before cooldown elapses must be blocked.
	if ok, _ := th.Allow("k"); ok {
		t.Error("expected throttle after 3 failures")
	}
}

func TestThrottleResetClearsState(t *testing.T) {
	th := NewLoginThrottle(time.Minute, 3, time.Minute)
	for i := 0; i < 5; i++ {
		th.RecordFailure("k")
	}
	if ok, _ := th.Allow("k"); ok {
		t.Fatal("expected throttle before reset")
	}
	th.Reset("k")
	if ok, _ := th.Allow("k"); !ok {
		t.Error("reset should clear throttle")
	}
}

func TestThrottlePerKeyIndependent(t *testing.T) {
	th := NewLoginThrottle(time.Minute, 3, time.Minute)
	for i := 0; i < 3; i++ {
		th.RecordFailure("alice")
	}
	if ok, _ := th.Allow("alice"); ok {
		t.Error("alice should be throttled")
	}
	if ok, _ := th.Allow("bob"); !ok {
		t.Error("bob should be unaffected by alice's failures")
	}
}
