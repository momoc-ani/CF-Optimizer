package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/benchmark"
	"github.com/cf-optimizer/cf-optimizer/internal/ipc"
	"github.com/cf-optimizer/cf-optimizer/internal/optimizer"
	"github.com/cf-optimizer/cf-optimizer/internal/store"
)

func TestDecodeStrictRejectsUnknownFieldAndTrailingValue(t *testing.T) {
	var target struct {
		Value bool `json:"value"`
	}
	if err := decodeStrict(json.RawMessage(`{"unknown":true}`), &target); err == nil {
		t.Fatal("expected unknown field error")
	}
	if err := decodeStrict(json.RawMessage(`{"value":true} {}`), &target); err == nil {
		t.Fatal("expected trailing value error")
	}
}

func TestActiveEventIsClonedAndClearedWithRun(t *testing.T) {
	api := &API{}
	progress := benchmark.Progress{Completed: 4, Total: 12}
	event := optimizer.Event{RunID: "run-1", Type: "benchmark.progress", Progress: &progress}
	api.setActiveEvent(event)

	progress.Completed = 9
	actual := cloneEvent(api.activeEvent)
	if actual == nil || actual.Progress == nil || actual.Progress.Completed != 4 {
		t.Fatalf("active event was not isolated from caller mutation: %#v", actual)
	}

	api.clearActiveCancel()
	if api.activeEvent != nil {
		t.Fatal("active event should be cleared after the run finishes")
	}
}

func TestSystemStatusExcludesLargeInternalState(t *testing.T) {
	stateStore, err := store.Open(t.TempDir(), 10)
	if err != nil {
		t.Fatal(err)
	}
	nodes := make(map[string]store.NodeStats, 5_000)
	for index := 0; index < 5_000; index++ {
		nodes[fmt.Sprintf("node-%05d-with-historical-statistics", index)] = store.NodeStats{Attempts: index + 1}
	}
	current := &store.Selection{IP: "104.21.94.176", Family: 4, PolicyVerified: true}
	largeReceipts := json.RawMessage(`{"backup":"` + strings.Repeat("x", 2<<20) + `"}`)
	if err := stateStore.Update(func(state *store.State) error {
		state.CurrentIPv4 = current
		state.Nodes = nodes
		state.History = []store.RunSummary{{ID: "run-sensitive-history"}}
		state.DiscoveredDomains["ani.momoc.top"] = store.DomainDiscovery{Domain: "ani.momoc.top", Active: true}
		state.Policy = &store.PolicySnapshot{Receipts: largeReceipts, AppliedAt: time.Now().UTC()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	api, err := NewAPI(&Runtime{Store: stateStore})
	if err != nil {
		t.Fatal(err)
	}
	response, err := api.Handle(context.Background(), ipc.Request{Method: "system.status"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) >= 64<<10 {
		t.Fatalf("system.status response is unexpectedly large: %d bytes", len(encoded))
	}
	responseText := string(encoded)
	for _, internalField := range []string{`"history"`, `"nodes"`, `"discovered_domains"`, `"policy"`, `"receipts"`, "run-sensitive-history"} {
		if strings.Contains(responseText, internalField) {
			t.Fatalf("system.status exposed internal field %s", internalField)
		}
	}
	if !strings.Contains(responseText, "104.21.94.176") {
		t.Fatal("system.status omitted the current verified selection")
	}
}
