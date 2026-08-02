package acceleration

import (
	"reflect"
	"testing"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/store"
)

func TestEffectiveDomainsMergesManualAuthorizedAndExcluded(t *testing.T) {
	cfg := config.Default()
	cfg.Acceleration.ManualDomains = []string{"Ani.Momoc.Top", "manual.example"}
	cfg.Acceleration.ExcludedDomains = []string{"manual.example", "blocked.example"}
	cfg.Acceleration.AutoApply = true
	cfg.Hosts.Domains = []string{"legacy.example"}
	state := store.State{DiscoveredDomains: map[string]store.DomainDiscovery{
		"auto.example":    {Domain: "auto.example", CloudflareVerified: true, PreflightVerified: true, Active: true, LastResolvedAddresses: []string{"104.18.1.2"}},
		"pending.example": {Domain: "pending.example", CloudflareVerified: true, PreflightVerified: false, Active: true},
		"blocked.example": {Domain: "blocked.example", CloudflareVerified: true, PreflightVerified: true, Active: true},
	}}

	got := EffectiveDomains(cfg, state)
	want := []string{"ani.momoc.top", "auto.example", "legacy.example"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EffectiveDomains() = %#v, want %#v", got, want)
	}
}

func TestEffectiveDomainsKeepsDiscoveriesInactiveWhenAutoApplyIsDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.Acceleration.AutoApply = false
	state := store.State{DiscoveredDomains: map[string]store.DomainDiscovery{
		"auto.example": {Domain: "auto.example", CloudflareVerified: true, PreflightVerified: true, Active: true},
	}}

	if got := EffectiveDomains(cfg, state); !reflect.DeepEqual(got, []string{"ani.momoc.top"}) {
		t.Fatalf("EffectiveDomains() = %#v", got)
	}
}

func TestEffectiveDiscoveriesExcludesManualDomainsAndPreservesResolvedAddresses(t *testing.T) {
	cfg := config.Default()
	cfg.Acceleration.ManualDomains = []string{"manual.example"}
	cfg.Acceleration.AutoApply = true
	state := store.State{DiscoveredDomains: map[string]store.DomainDiscovery{
		"manual.example": {Domain: "manual.example", CloudflareVerified: true, PreflightVerified: true, Active: true, LastResolvedAddresses: []string{"104.18.1.1"}},
		"auto.example":   {Domain: "auto.example", CloudflareVerified: true, PreflightVerified: true, Active: true, LastResolvedAddresses: []string{"104.18.1.2"}},
	}}

	got := EffectiveDiscoveries(cfg, state)
	if len(got) != 1 || got[0].Domain != "auto.example" || !reflect.DeepEqual(got[0].LastResolvedAddresses, []string{"104.18.1.2"}) {
		t.Fatalf("EffectiveDiscoveries() = %#v", got)
	}
}
