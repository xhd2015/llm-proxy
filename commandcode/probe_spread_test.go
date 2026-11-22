//go:build probe

package commandcode

// One parallel round across one proxy per account. It answers the load
// balancer's premise: does spreading the same load over several accounts beat
// stacking it on one?
//
//	COMMANDCODE_PROBE=1 go test -tags probe -run TestProbeAccountSpread -v ./commandcode

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

func spreadTargets() []struct{ name, url string } {
	if value := os.Getenv("LLM_PROXY_ACCOUNT_URLS"); value != "" {
		var out []struct{ name, url string }
		for _, pair := range strings.Split(value, ",") {
			name, url, ok := strings.Cut(pair, "=")
			if ok {
				out = append(out, struct{ name, url string }{name, url})
			}
		}
		return out
	}
	return []struct{ name, url string }{
		{"xhd2015", "http://127.0.0.1:8891"},
		{"foxmail", "http://127.0.0.1:8892"},
		{"lukaz", "http://127.0.0.1:8893"},
		{"bytedanc", "http://127.0.0.1:8894"},
	}
}

func TestProbeAccountSpread(t *testing.T) {
	requireProbe(t)
	bodyPath := os.Getenv("LLM_PROXY_AB_BODY")
	if bodyPath == "" {
		bodyPath = "/tmp/long_body.json"
	}
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		t.Fatalf("read %s: %v", bodyPath, err)
	}
	targets := spreadTargets()
	results := make([]abResult, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = runABRound(t, target.name, target.url, body)
		}()
	}
	wg.Wait()

	var ok []float64
	for i, result := range results {
		t.Logf("%-10s %s", targets[i].name, result)
		if result.err == "" {
			ok = append(ok, result.headers.Seconds())
		}
	}
	t.Logf("RESULT: %d/%d accounts served in parallel; headers: %v", len(ok), len(targets), ok)
	fmt.Println()
}
