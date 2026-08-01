package xray

import (
	"encoding/json"
	"testing"

	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

func TestFormatUsesXrayDomainSyntax(t *testing.T) {
	content, count, err := format(proxy.DirectPolicy{Domains: []string{"example.com", "+.cloudflare.com"}}, "direct")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("unexpected rule count: %d", count)
	}
	var document struct {
		Routing struct {
			Rules []struct {
				Domain []string `json:"domain"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	want := []string{"full:example.com", "domain:cloudflare.com"}
	if len(document.Routing.Rules) != 1 || len(document.Routing.Rules[0].Domain) != 2 || document.Routing.Rules[0].Domain[0] != want[0] || document.Routing.Rules[0].Domain[1] != want[1] {
		t.Fatalf("unexpected Xray domains: %#v", document.Routing.Rules)
	}
}
