package mihomo

import "testing"

func TestActiveConfigMatchesUnixController(t *testing.T) {
	content := []byte("external-controller: ''\nexternal-controller-unix: /tmp/verge/verge-mihomo.sock\n")
	if !activeConfigMatchesController(content, "unix:///tmp/verge/verge-mihomo.sock") {
		t.Fatal("active config did not match its Unix controller")
	}
	if activeConfigMatchesController(content, "unix:///tmp/other.sock") {
		t.Fatal("active config matched an unrelated Unix controller")
	}
}
