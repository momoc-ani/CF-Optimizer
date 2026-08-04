package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadRunDetailReturnsPersistedDocument(t *testing.T) {
	directory := t.TempDir()
	stateStore, err := Open(directory, 10)
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`[{"ip":"104.25.250.104","score":93.31}]`)
	if err := stateStore.SaveRunDetail("run-1", payload, time.Hour); err != nil {
		t.Fatal(err)
	}

	detail, err := stateStore.LoadRunDetail("run-1")
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, detail.Payload); err != nil {
		t.Fatal(err)
	}
	if detail.Version != 1 || detail.RunID != "run-1" || detail.SavedAt.IsZero() || !bytes.Equal(compact.Bytes(), payload) {
		t.Fatalf("unexpected run detail: %#v", detail)
	}
}

func TestLoadRunDetailRejectsInvalidAndMismatchedRunIDs(t *testing.T) {
	stateStore, err := Open(t.TempDir(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stateStore.LoadRunDetail("../outside"); err == nil {
		t.Fatal("expected invalid run ID to fail")
	}

	directory := filepath.Join(stateStore.dataDir, "run-details")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	document := []byte(`{"version":1,"run_id":"other","saved_at":"2026-08-05T00:00:00Z","payload":[]}`)
	if err := os.WriteFile(filepath.Join(directory, "run-1.json"), document, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := stateStore.LoadRunDetail("run-1"); err == nil {
		t.Fatal("expected mismatched document run ID to fail")
	}
}

func TestUpdatePersistsAndTrimsHistory(t *testing.T) {
	directory := t.TempDir()
	stateStore, err := Open(directory, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := stateStore.Update(func(state *State) error {
		state.History = []RunSummary{{ID: "1"}, {ID: "2"}, {ID: "3"}}
		state.Nodes["1.1.1.1"] = NodeStats{Attempts: 1, Successes: 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(filepath.Dir(stateStore.path), 2)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := reopened.Snapshot()
	if len(snapshot.History) != 2 || snapshot.History[0].ID != "2" {
		t.Fatalf("history was not trimmed: %#v", snapshot.History)
	}
	if snapshot.Nodes["1.1.1.1"].Successes != 1 {
		t.Fatalf("node stats were not persisted: %#v", snapshot.Nodes)
	}
}

func TestOpenMigratesVersionOneStateAndPersistsPendingTransaction(t *testing.T) {
	directory := t.TempDir()
	legacy := []byte(`{"version":1,"history":[],"nodes":{},"discovered_domains":{},"running":false}`)
	if err := os.WriteFile(filepath.Join(directory, "state.json"), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	stateStore, err := Open(directory, 10)
	if err != nil {
		t.Fatal(err)
	}
	policy := json.RawMessage(`{"ipv4_cidrs":["1.1.1.1/32"]}`)
	receipts := json.RawMessage(`{"receipts":[]}`)
	if err := stateStore.Update(func(state *State) error {
		state.PendingPolicy = NewPolicyTransaction(time.Unix(10, 0), policy, receipts)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory, 10)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := reopened.Snapshot()
	if snapshot.Version != StateSchemaVersion || snapshot.PendingPolicy == nil || string(snapshot.PendingPolicy.Receipts) != string(receipts) {
		t.Fatalf("version one state was not migrated with its transaction: %#v", snapshot)
	}
}
