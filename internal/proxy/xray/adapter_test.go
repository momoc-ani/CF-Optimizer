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

func TestFormatIncludesDomainMappingDNSAndDirectRoute(t *testing.T) {
	content, _, err := format(proxy.DirectPolicy{
		Domains:        []string{"ani.momoc.top"},
		DomainMappings: []proxy.DomainMapping{{Domain: "ani.momoc.top", Addresses: []string{"104.17.158.152"}}},
	}, "direct")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		DNS struct {
			Hosts map[string][]string `json:"hosts"`
		} `json:"dns"`
		Routing struct {
			Rules []struct {
				Domain      []string `json:"domain"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	addresses := document.DNS.Hosts["ani.momoc.top"]
	if len(addresses) != 1 || addresses[0] != "104.17.158.152" {
		t.Fatalf("unexpected Xray DNS mapping: %#v", document.DNS.Hosts)
	}
	if len(document.Routing.Rules) != 1 || document.Routing.Rules[0].OutboundTag != "direct" || document.Routing.Rules[0].Domain[0] != "full:ani.momoc.top" {
		t.Fatalf("unexpected Xray direct route: %#v", document.Routing.Rules)
	}
}
