package optimizer

import (
	"encoding/json"
	"testing"

	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
	"github.com/cf-optimizer/cf-optimizer/internal/store"
)

func TestPolicyReceiptJournalKeepsEarlierTransitionWhenLaterApplyRollsBack(t *testing.T) {
	stateStore, err := store.Open(t.TempDir(), 10)
	if err != nil {
		t.Fatal(err)
	}
	journal := newPolicyReceiptJournal(stateStore)
	policy := proxy.DirectPolicy{IPv4CIDRs: []string{"1.1.1.1/32"}}
	transition := proxy.Receipt{ID: "transition", Adapter: "test", Changed: true}
	final := proxy.Receipt{ID: "final", Adapter: "test", Changed: true}
	if err := journal.Begin(policy); err != nil {
		t.Fatal(err)
	}
	if err := journal.Record(transition); err != nil {
		t.Fatal(err)
	}
	if err := journal.Begin(policy); err != nil {
		t.Fatal(err)
	}
	if err := journal.Record(final); err != nil {
		t.Fatal(err)
	}
	if err := journal.Remove([]proxy.Receipt{final}); err != nil {
		t.Fatal(err)
	}
	pending := stateStore.Snapshot().PendingPolicy
	if pending == nil {
		t.Fatal("rolling back the final apply removed the earlier transition journal")
	}
	var applied proxy.ApplyResult
	if err := json.Unmarshal(pending.Receipts, &applied); err != nil {
		t.Fatal(err)
	}
	if len(applied.Receipts) != 1 || applied.Receipts[0].ID != transition.ID {
		t.Fatalf("unexpected remaining journal receipts: %#v", applied.Receipts)
	}
	if err := journal.Remove([]proxy.Receipt{transition}); err != nil {
		t.Fatal(err)
	}
	if stateStore.Snapshot().PendingPolicy != nil {
		t.Fatal("empty policy journal was not removed")
	}
}
