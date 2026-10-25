package ai_test

import (
	"os/exec"
	"strings"
	"testing"
)

// providerSDKs are module prefixes that may only be imported by adapters.
var providerSDKs = []string{
	"github.com/anthropics/",
	"github.com/openai/",
	"github.com/sashabaranov/go-openai",
	"google.golang.org/genai",
}

// TestProviderCodeStaysInAdapters fails when any package outside
// internal/ai/providers depends on a provider SDK, or when business logic
// depends on an adapter. Only the composition root may wire adapters in.
func TestProviderCodeStaysInAdapters(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not available")
	}
	modOut, err := exec.Command(goBin, "list", "-m").Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	module := strings.TrimSpace(string(modOut))
	adapters := module + "/internal/ai/providers"
	mayWireAdapters := map[string]bool{module + "/cmd/server": true}

	out, err := exec.Command(goBin, "list", "-f", `{{.ImportPath}} {{join .Deps " "}}`, module+"/...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		pkg, deps := fields[0], fields[1:]
		if strings.HasPrefix(pkg, adapters) {
			continue
		}
		for _, dep := range deps {
			for _, sdk := range providerSDKs {
				if strings.HasPrefix(dep, sdk) {
					t.Errorf("%s depends on provider SDK %s", pkg, dep)
				}
			}
			if strings.HasPrefix(dep, adapters) && !mayWireAdapters[pkg] {
				t.Errorf("%s depends on adapter %s; business logic must use internal/ai only", pkg, dep)
			}
		}
	}
}
