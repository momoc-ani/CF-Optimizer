//go:build windows

package mihomo

import "testing"

func TestDecodeWindowsControllerCandidatesFiltersNonLoopbackAndNonMihomo(t *testing.T) {
	output := []byte(`{
		"Processes": [],
		"Listeners": [
			{"LocalAddress":"0.0.0.0","LocalPort":9097,"ProcessName":"verge-mihomo"},
			{"LocalAddress":"192.168.1.2","LocalPort":9098,"ProcessName":"mihomo"},
			{"LocalAddress":"127.0.0.1","LocalPort":8080,"ProcessName":"other"}
		]
	}`)
	candidates, err := decodeWindowsControllerCandidates(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Controller != "http://127.0.0.1:9097" {
		t.Fatalf("unexpected candidates: %#v", candidates)
	}
}

func TestDecodeWindowsControllerCandidatesIncludesNamedPipeCommandLine(t *testing.T) {
	output := []byte(`{
		"Processes": [{
			"ProcessName":"verge-mihomo.exe",
			"CommandLine":"\"C:\\Program Files\\Clash Verge\\verge-mihomo.exe\" -d C:\\Users\\demo\\AppData\\Roaming\\clash -f config.yaml -ext-ctl-pipe \\\\.\\pipe\\verge-mihomo"
		}],
		"Listeners": []
	}`)
	candidates, err := decodeWindowsControllerCandidates(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("unexpected candidates: %#v", candidates)
	}
	candidate := candidates[0]
	if candidate.Controller != "npipe:////./pipe/verge-mihomo" || candidate.Process != "verge-mihomo" {
		t.Fatalf("unexpected Named Pipe candidate: %#v", candidate)
	}
	wantConfig := `C:\Users\demo\AppData\Roaming\clash\config.yaml`
	if candidate.ConfigPath != wantConfig {
		t.Fatalf("config path = %q, want %q", candidate.ConfigPath, wantConfig)
	}
}

func TestWindowsCommandLineFlagUsesLastCompatibleForm(t *testing.T) {
	arguments := []string{"mihomo.exe", "-ext-ctl-pipe", `\\.\pipe\old`, `--ext-ctl-pipe=\\.\pipe\current`}
	if got := windowsCommandLineFlag(arguments, "-ext-ctl-pipe", "--ext-ctl-pipe"); got != `\\.\pipe\current` {
		t.Fatalf("flag value = %q", got)
	}
}
