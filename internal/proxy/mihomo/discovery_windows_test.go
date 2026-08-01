//go:build windows

package mihomo

import "testing"

func TestDecodeWindowsControllerCandidatesFiltersNonLoopbackAndNonMihomo(t *testing.T) {
	output := []byte(`[
		{"LocalAddress":"0.0.0.0","LocalPort":9097,"ProcessName":"verge-mihomo"},
		{"LocalAddress":"192.168.1.2","LocalPort":9098,"ProcessName":"mihomo"},
		{"LocalAddress":"127.0.0.1","LocalPort":8080,"ProcessName":"other"}
	]`)
	candidates, err := decodeWindowsControllerCandidates(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Controller != "http://127.0.0.1:9097" {
		t.Fatalf("unexpected candidates: %#v", candidates)
	}
}
