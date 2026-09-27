package web

import (
	"testing"
	"time"
)

func TestLoginLimiterBlocksAfterMaxFailures(t *testing.T) {
	l := newLoginLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if l.blocked("ip:1", "email:a") {
			t.Fatalf("blocked after only %d failures", i)
		}
		l.fail("ip:1", "email:a")
	}
	if !l.blocked("ip:1", "email:a") {
		t.Fatal("expected a block after 3 failures")
	}
	// The same email from a new IP is still blocked: the per-email key caps
	// guessing against one account from many machines.
	if !l.blocked("ip:2", "email:a") {
		t.Fatal("expected the per-email limit to apply across IPs")
	}
	l.reset("ip:1", "email:a")
	if l.blocked("ip:1", "email:a") {
		t.Fatal("expected a successful login to reset the counters")
	}
}
