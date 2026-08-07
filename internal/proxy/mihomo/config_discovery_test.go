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

func TestActiveConfigMatchesNamedPipeController(t *testing.T) {
	content := []byte("external-controller: ''\nexternal-controller-pipe: \\\\.\\pipe\\verge-mihomo\n")
	controller, err := namedPipeControllerEndpoint(`\\.\pipe\verge-mihomo`)
	if err != nil {
		t.Fatal(err)
	}
	if controller != "npipe:////./pipe/verge-mihomo" {
		t.Fatalf("Named Pipe controller = %q", controller)
	}
	if !activeConfigMatchesController(content, controller) {
		t.Fatal("active config did not match its Windows Named Pipe controller")
	}
	if activeConfigMatchesController(content, "npipe:////./pipe/other") {
		t.Fatal("active config matched an unrelated Windows Named Pipe controller")
	}
}
