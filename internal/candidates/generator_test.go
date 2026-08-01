package candidates

import (
	"net/netip"
	"reflect"
	"testing"

	"github.com/cf-optimizer/cf-optimizer/internal/ranges"
)

func TestGenerateIsDeterministicAndUnique(t *testing.T) {
	snapshot := ranges.Builtin()
	a, err := Generate(snapshot, Options{Count: 200, IPv4: true, IPv6: true, Seed: "day-1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(snapshot, Options{Count: 200, IPv4: true, IPv6: true, Seed: "day-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same seed produced different candidates")
	}
	seen := map[netip.Addr]bool{}
	for _, addr := range a {
		if seen[addr] {
			t.Fatalf("duplicate address %s", addr)
		}
		seen[addr] = true
	}
}

func TestPreferredAndExcluded(t *testing.T) {
	snapshot := ranges.Builtin()
	snapshot.Exclude = []string{"104.16.0.0/16"}
	preferred := netip.MustParseAddr("104.16.1.1")
	values, err := Generate(snapshot, Options{Count: 20, IPv4: true, Seed: "x", Preferred: []netip.Addr{preferred}})
	if err != nil {
		t.Fatal(err)
	}
	for _, addr := range values {
		if netip.MustParsePrefix("104.16.0.0/16").Contains(addr) {
			t.Fatalf("excluded address generated: %s", addr)
		}
	}
}

func TestCooldownSkipsRandomCandidateButKeepsPreferred(t *testing.T) {
	snapshot := ranges.Snapshot{Version: 1, IPv4: []string{"1.1.1.1/32"}}
	address := netip.MustParseAddr("1.1.1.1")
	values, err := Generate(snapshot, Options{
		Count: 1, IPv4: true, Seed: "x", Preferred: []netip.Addr{address}, Cooldown: []netip.Addr{address},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0] != address {
		t.Fatalf("current preferred node must bypass cooldown: %v", values)
	}
}
