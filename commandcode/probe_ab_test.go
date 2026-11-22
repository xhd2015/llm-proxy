//go:build probe

package commandcode

// A/B probe for the commandcode session id: the same long body is sent to two
// llm-proxy instances, one started with --commandcode-session=random and one
// with =derive. Two rounds each show whether a stable threadId keeps the
// upstream prompt cache warm:
//
//	random #1  cold   (new id, new prefix)
//	derive #1  warm only if the cache is content-keyed, cold if session-keyed
//	random #2  warm only if the cache is content-keyed
//	derive #2  warm under both hypotheses
//
// Opt in, because every round is a real request against the subscription:
//
//	COMMANDCODE_PROBE=1 go test -tags probe -run TestProbeSessionIDAB -v ./commandcode

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type abResult struct {
	round       string
	status      int
	headers     time.Duration
	firstDelta  time.Duration
	total       time.Duration
	inputTokens int
	cacheRead   int
	deltas      int
	err         string
}

var (
	inputRe  = regexp.MustCompile(`"input_tokens":(\d+)`)
	cacheRe  = regexp.MustCompile(`"cache_read_input_tokens":(\d+)`)
	numberRe = regexp.MustCompile(`\d+`)
)

func (r abResult) String() string {
	if r.err != "" {
		return fmt.Sprintf("%-12s ERROR %s", r.round, r.err)
	}
	return fmt.Sprintf("%-12s status=%d headers=%5.1fs firstDelta=%5.1fs total=%5.1fs deltas=%3d input=%7d cacheRead=%7d (%.1f%%)",
		r.round, r.status, r.headers.Seconds(), r.firstDelta.Seconds(), r.total.Seconds(), r.deltas,
		r.inputTokens, r.cacheRead, 100*float64(r.cacheRead)/float64(max(r.inputTokens, 1)))
}

// runABRound sends one request to base and records where its time went.
func runABRound(t *testing.T, round, base string, body []byte) abResult {
	t.Helper()
	result := abResult{round: round}
	// The long body is slow to build and the upstream may think for minutes.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/messages", strings.NewReader(string(body)))
	if err != nil {
		result.err = err.Error()
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "probe")
	req.Header.Set("anthropic-version", "2023-06-01")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		result.err = err.Error()
		return result
	}
	defer resp.Body.Close()
	result.status = resp.StatusCode
	result.headers = time.Since(start)
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		result.err = strings.TrimSpace(string(payload))
		return result
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			if strings.TrimSpace(strings.TrimPrefix(line, "event: ")) == "content_block_delta" {
				result.deltas++
				if result.firstDelta == 0 {
					result.firstDelta = time.Since(start)
				}
			}
			continue
		}
		if !strings.Contains(line, `"usage"`) {
			continue
		}
		// message_start carries zeros; the terminal message_delta carries the
		// real counters, so keep the maxima rather than the last value.
		if match := inputRe.FindStringSubmatch(line); match != nil {
			if value := atoi(match[1]); value > result.inputTokens {
				result.inputTokens = value
			}
		}
		if match := cacheRe.FindStringSubmatch(line); match != nil {
			if value := atoi(match[1]); value > result.cacheRead {
				result.cacheRead = value
			}
		}
	}
	result.total = time.Since(start)
	if err := scanner.Err(); err != nil && result.err == "" {
		result.err = err.Error()
	}
	return result
}

func atoi(value string) int {
	digits := numberRe.FindString(value)
	n := 0
	for _, r := range digits {
		n = n*10 + int(r-'0')
	}
	return n
}

// TestProbeSessionIDAB compares the two session modes on one long body.
func TestProbeSessionIDAB(t *testing.T) {
	requireProbe(t)
	deriveURL := os.Getenv("LLM_PROXY_DERIVE_URL")
	if deriveURL == "" {
		deriveURL = "http://127.0.0.1:8895"
	}
	randomURL := os.Getenv("LLM_PROXY_RANDOM_URL")
	if randomURL == "" {
		randomURL = "http://127.0.0.1:8896"
	}
	bodyPath := os.Getenv("LLM_PROXY_AB_BODY")
	if bodyPath == "" {
		bodyPath = "/tmp/long_body.json"
	}
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		t.Fatalf("read %s: %v (start the two probe proxies first)", bodyPath, err)
	}
	t.Logf("body: %s (%.1f MB)", bodyPath, float64(len(body))/1048576)

	// One cold round per mode establishes the prefix; the comparison is run on
	// repeated rounds afterwards, interleaved so queue drift hits both modes.
	cold := []struct {
		round string
		base  string
	}{
		{"random cold", randomURL},
		{"derive cold", deriveURL},
	}
	for _, round := range cold {
		result := runABRound(t, round.round, round.base, body)
		t.Logf("%s", result)
		if result.err != "" {
			t.Fatalf("%s failed: %s", round.round, result.err)
		}
	}

	const repeats = 10
	randomRounds := make([]abResult, 0, repeats)
	deriveRounds := make([]abResult, 0, repeats)
	for i := range repeats {
		for _, round := range []struct {
			round string
			base  string
			into  *[]abResult
		}{
			{fmt.Sprintf("random %d", i+1), randomURL, &randomRounds},
			{fmt.Sprintf("derive %d", i+1), deriveURL, &deriveRounds},
		} {
			result := runABRound(t, round.round, round.base, body)
			t.Logf("%s", result)
			if result.err != "" {
				t.Fatalf("%s failed: %s", round.round, result.err)
			}
			*round.into = append(*round.into, result)
		}
	}

	report := func(name string, rounds []abResult) (median float64, hits float64) {
		values := make([]float64, 0, len(rounds))
		for _, r := range rounds {
			values = append(values, r.headers.Seconds())
			hits += 100 * float64(r.cacheRead) / float64(max(r.inputTokens, 1))
		}
		sort.Float64s(values)
		t.Logf("%s: headers median=%.2fs min=%.2fs max=%.2fs cacheRead avg=%.1f%%",
			name, values[len(values)/2], values[0], values[len(values)-1], hits/float64(len(rounds)))
		return values[len(values)/2], hits / float64(len(rounds))
	}
	randomMedian, randomHit := report("random", randomRounds)
	deriveMedian, deriveHit := report("derive", deriveRounds)
	t.Logf("RESULT: stable session id headers %.2fs vs %.2fs (%.0f%% faster), cache hit %.1f%% vs %.1f%%",
		deriveMedian, randomMedian, 100*(randomMedian-deriveMedian)/randomMedian, deriveHit, randomHit)

	if deriveHit < 50 {
		t.Errorf("derive mode read only %.1f%% from cache", deriveHit)
	}
}

// TestProbeSessionConcurrency measures the same warm body under parallel load.
// If TTFB climbs while the prompt is unchanged and fully cached, the queue, not
// the context, is what costs minutes in production.
func TestProbeSessionConcurrency(t *testing.T) {
	requireProbe(t)
	base := os.Getenv("LLM_PROXY_DERIVE_URL")
	if base == "" {
		base = "http://127.0.0.1:8895"
	}
	bodyPath := os.Getenv("LLM_PROXY_AB_BODY")
	if bodyPath == "" {
		bodyPath = "/tmp/long_body.json"
	}
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		t.Fatalf("read %s: %v", bodyPath, err)
	}

	warm := runABRound(t, "warm", base, body)
	t.Logf("%s", warm)

	const parallel = 4
	results := make([]abResult, parallel)
	var wg sync.WaitGroup
	for i := range parallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = runABRound(t, fmt.Sprintf("parallel %d", i+1), base, body)
		}()
	}
	wg.Wait()

	headers := make([]float64, 0, parallel)
	worst := 0.0
	for _, result := range results {
		t.Logf("%s", result)
		if result.err != "" {
			t.Errorf("%s failed: %s", result.round, result.err)
			continue
		}
		headers = append(headers, result.headers.Seconds())
		if result.headers.Seconds() > worst {
			worst = result.headers.Seconds()
		}
	}
	sort.Float64s(headers)
	if len(headers) > 0 {
		t.Logf("RESULT: warm single=%.2fs | %d parallel: median=%.2fs max=%.2fs",
			warm.headers.Seconds(), parallel, headers[len(headers)/2], worst)
	}
}
