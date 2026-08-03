//go:build darwin

package mihomo

import "testing"

func TestParseDarwinControllerCandidatesIncludesCoreUnixSocket(t *testing.T) {
	output := "p100\ncclash-verge\nf7\nn/var/folders/session.sock\n" +
		"p200\ncverge-mihomo\nf6\nn/tmp/verge/verge-mihomo.sock\nf7\nn*:7897\n"
	candidates := parseDarwinControllerCandidates(output)
	if len(candidates) != 2 {
		t.Fatalf("candidate count = %d, want 2: %#v", len(candidates), candidates)
	}
	if candidates[0].Controller != "unix:///tmp/verge/verge-mihomo.sock" || candidates[0].Process != "verge-mihomo" {
		t.Fatalf("unexpected Unix candidate: %#v", candidates[0])
	}
	if candidates[1].Controller != "http://127.0.0.1:7897" {
		t.Fatalf("unexpected TCP candidate: %#v", candidates[1])
	}
}
