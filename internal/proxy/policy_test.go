package proxy

import "testing"

func TestPolicyNormalizeRejectsInjectionAndDeduplicates(t *testing.T) {
	policy, err := (DirectPolicy{
		IPv4CIDRs: []string{"1.1.1.1/32", "1.1.1.1/32"},
		Domains:   []string{"Example.COM", "example.com"},
		Processes: []string{"cf-optimizer"},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.IPv4CIDRs) != 1 || len(policy.Domains) != 1 || policy.Domains[0] != "example.com" {
		t.Fatalf("policy was not normalized: %#v", policy)
	}
	if _, err := (DirectPolicy{Processes: []string{"bad,PROCESS"}}).Normalize(); err == nil {
		t.Fatal("expected process injection to be rejected")
	}
}
