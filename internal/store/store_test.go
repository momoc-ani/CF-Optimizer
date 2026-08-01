package store

import (
	"path/filepath"
	"testing"
)

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
