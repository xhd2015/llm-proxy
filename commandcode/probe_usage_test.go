//go:build probe

package commandcode

// Read-only probe: can each Command Code account's quota be polled on its own?
// This is the load balancer's only source of "account still has capacity".
//
//	COMMANDCODE_PROBE=1 go test -tags probe -run TestProbeAccountUsage -v ./commandcode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type probeCredits struct {
	Credits struct {
		MonthlyCredits   float64 `json:"monthlyCredits"`
		PurchasedCredits float64 `json:"purchasedCredits"`
		FreeCredits      float64 `json:"freeCredits"`
		BelowThreshold   bool    `json:"belowThreshold"`
	} `json:"credits"`
	WindowLimits struct {
		Limited  bool `json:"limited"`
		Exceeded any  `json:"exceeded"`
		FiveHour *struct {
			Used float64 `json:"used"`
			Cap  float64 `json:"cap"`
		} `json:"fiveHour"`
		Weekly *struct {
			Used float64 `json:"used"`
			Cap  float64 `json:"cap"`
		} `json:"weekly"`
	} `json:"windowLimits"`
}

func probeHomes() []string {
	if value := os.Getenv("COMMANDCODE_PROBE_HOMES"); value != "" {
		return strings.Split(value, ":")
	}
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, ".sandbox", "commandcode-xhd2015-20261029", ".commandcode"),
		filepath.Join(home, ".sandbox", "commandcode-foxmail-20261029", ".commandcode"),
		filepath.Join(home, ".sandbox", "commandcode-lukaz", ".commandcode"),
		filepath.Join(home, ".sandbox", "commandcode-foxmail-bytedanc", ".commandcode"),
	}
}

func TestProbeAccountUsage(t *testing.T) {
	requireProbe(t)
	for _, home := range probeHomes() {
		name := filepath.Base(filepath.Dir(home))
		auth, err := ReadAuth(home)
		if err != nil {
			t.Logf("%-38s auth: %v", name, err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, DefaultBaseURL+"/alpha/billing/credits", nil)
		if err != nil {
			cancel()
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+auth.APIKey)
		req.Header.Set("User-Agent", "cli")
		req.Header.Set("X-Command-Code-Version", probeVersion())
		req.Header.Set("X-Cli-Environment", "production")
		start := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Logf("%-38s request: %v", name, err)
			continue
		}
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		cancel()
		if resp.StatusCode != http.StatusOK {
			t.Logf("%-38s status=%s body=%.160s", name, resp.Status, payload)
			continue
		}
		var credits probeCredits
		if err := json.Unmarshal(payload, &credits); err != nil {
			t.Logf("%-38s decode: %v (%.160s)", name, err, payload)
			continue
		}
		five, weekly := "n/a", "n/a"
		if credits.WindowLimits.FiveHour != nil {
			five = fmt.Sprintf("%.0f/%.0f", credits.WindowLimits.FiveHour.Used, credits.WindowLimits.FiveHour.Cap)
		}
		if credits.WindowLimits.Weekly != nil {
			weekly = fmt.Sprintf("%.0f/%.0f", credits.WindowLimits.Weekly.Used, credits.WindowLimits.Weekly.Cap)
		}
		t.Logf("%-38s ok in %4dms credits monthly=%.0f purchased=%.0f free=%.0f limited=%v exceeded=%v fiveHour=%s weekly=%s",
			name, time.Since(start).Milliseconds(),
			credits.Credits.MonthlyCredits, credits.Credits.PurchasedCredits, credits.Credits.FreeCredits,
			credits.WindowLimits.Limited, credits.WindowLimits.Exceeded, five, weekly)
	}
}
