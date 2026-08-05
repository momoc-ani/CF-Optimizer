package windows

import (
	"os"
	"strings"
	"testing"
)

func TestInstallerRepairsServiceAfterFilesAreReplaced(t *testing.T) {
	content, err := os.ReadFile("installer.iss")
	if err != nil {
		t.Fatal(err)
	}
	script := string(content)
	for _, expected := range []string{
		"AfterInstall: ConfigureService",
		"procedure ConfigureService;",
		"install --daemon",
		"(ExitCode <> 0)",
		`Source: "..\..\config.example.yaml"; DestDir: "{commonappdata}\CF Optimizer"; DestName: "config.yaml"; Flags: ignoreversion onlyifdoesntexist`,
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("installer is missing repair guard %q", expected)
		}
	}
	if strings.Contains(script, "Check: IsFreshInstall") || strings.Contains(script, "Check: IsServiceUpgrade") {
		t.Fatal("installer still relies on service state cached before file replacement")
	}
}

func TestInstallerAllowsAnExistingStoppedService(t *testing.T) {
	content, err := os.ReadFile("installer.iss")
	if err != nil {
		t.Fatal(err)
	}
	script := string(content)
	for _, expected := range []string{
		"function QueryServiceRunning",
		"if ServiceRunning then",
		"Unable to query the existing CF Optimizer service.",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("installer is missing stopped-service handling %q", expected)
		}
	}
}
