package daemon

import (
	"testing"
	"time"
)

func TestExponentialDelayIsBounded(t *testing.T) {
	maximum := 6 * time.Hour
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute}
	for index, expected := range want {
		if actual := exponentialDelay(index+1, maximum); actual != expected {
			t.Fatalf("failure %d: got %s, want %s", index+1, actual, expected)
		}
	}
	if actual := exponentialDelay(100, maximum); actual != maximum {
		t.Fatalf("delay exceeded bound: %s", actual)
	}
}
