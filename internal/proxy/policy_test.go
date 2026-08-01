package proxy

import "testing"

func TestPolicyNormalizeRejectsInjectionAndDeduplicates(t *testing.T) {
	policy, err := (DirectPolicy{
		IPv4CIDRs: []string{"1.1.1.1/32", "1.1.1.1/32"},
		Domains:   []string{"Example.COM", "example.com"},
		Processes: []string{"cf-optimizer"},
		DomainMappings: []DomainMapping{
			{Domain: "Example.COM", Addresses: []string{"2606:4700::1", "1.1.1.1", "1.1.1.1"}},
		},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.IPv4CIDRs) != 1 || len(policy.Domains) != 1 || policy.Domains[0] != "example.com" || len(policy.DomainMappings) != 1 || len(policy.DomainMappings[0].Addresses) != 2 {
		t.Fatalf("policy was not normalized: %#v", policy)
	}
	if _, err := (DirectPolicy{Processes: []string{"bad,PROCESS"}}).Normalize(); err == nil {
		t.Fatal("expected process injection to be rejected")
	}
	if _, err := (DirectPolicy{DomainMappings: []DomainMapping{{Domain: "*.example.com", Addresses: []string{"1.1.1.1"}}}}).Normalize(); err == nil {
		t.Fatal("expected wildcard mapping to be rejected")
	}
}
