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
		"auto.example":    {Domain: "auto.example", CloudflareVerified: true, PreflightVerified: true, Active: true},
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
