//go:build probe

package commandcode

// Network probes against the real Command Code backend, to answer whether
// /alpha/generate supports anything other than "full history per POST":
//
//   - WebSocket (101 Switching Protocols) instead of plain POST streaming
//   - server-side thread continuation: a stable threadId plus only the new
//     message, instead of resending every prior message
//
// Opt-in, because they hit the network and the user's subscription:
//
//	COMMANDCODE_PROBE=1 go test -tags probe -run TestProbe -v ./commandcode
//
// The Command Code backend rejects requests that do not look like its own CLI,
// so every probe carries the same identity headers the proxy sends
// (User-Agent: cli, X-Command-Code-Version, X-Cli-Environment) and the real
// apiKey from the sandbox home.

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func probeHome() string {
	if home := os.Getenv("COMMANDCODE_PROBE_HOME"); home != "" {
		return home
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".sandbox", "commandcode-xhd2015-20261029", ".commandcode")
}

func probeVersion() string {
	if version := os.Getenv("COMMANDCODE_PROBE_VERSION"); version != "" {
		return version
	}
	return DefaultVersion
}

func requireProbe(t *testing.T) {
	t.Helper()
	if os.Getenv("COMMANDCODE_PROBE") != "1" {
		t.Skip("set COMMANDCODE_PROBE=1 to run network probes against api.commandcode.ai")
	}
	auth, err := ReadAuth(probeHome())
	if err != nil {
		t.Fatalf("read auth from %s: %v", probeHome(), err)
	}
	if auth.APIKey == "" {
		t.Fatalf("empty apiKey in %s", probeHome())
	}
}

func probeRequest(threadID, text string, maxTokens int) *alphaRequest {
	return &alphaRequest{
		Config: alphaConfig{
			WorkingDir:    "/tmp",
			Date:          time.Now().Format("2006-01-02"),
			Environment:   "darwin",
			Structure:     []string{},
			RecentCommits: []string{},
		},
		Params: alphaParams{
			Stream:    true,
			MaxTokens: maxTokens,
			Model:     "deepseek/deepseek-v4.1-flash",
			Tools:     []alphaTool{},
			System:    "You are a terse assistant. Answer with the requested token only.",
			Messages: []alphaMessage{{
				Role:    "user",
				Content: []alphaBlock{{Type: "text", Text: text}},
			}},
		},
		ThreadID: threadID,
	}
}

// generate sends one request and returns the raw stream body.
func generate(t *testing.T, req *alphaRequest) string {
	t.Helper()
	client := &Client{
		BaseURL: DefaultBaseURL,
		Home:    probeHome(),
		Version: probeVersion(),
		HTTP:    &http.Client{Timeout: 120 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	resp, err := client.Generate(ctx, req)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	t.Logf("status=%s bytes=%d", resp.Status, len(body))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upstream rejected the probe: %s: %s", resp.Status, string(body))
	}
	return string(body)
}

// TestProbeGenerateSanity proves the probe itself is accepted: without this,
// a 401/403 on every other probe would look like "WebSocket unsupported".
func TestProbeGenerateSanity(t *testing.T) {
	requireProbe(t)
	threadID, err := newThreadID()
	if err != nil {
		t.Fatalf("newThreadID: %v", err)
	}
	body := generate(t, probeRequest(threadID, "Reply with the single word: ping", 32))
	if !strings.Contains(body, "text-delta") && !strings.Contains(body, "text-start") {
		t.Fatalf("expected an AI SDK text part in the stream, got: %.300s", body)
	}
}

// TestProbeWebSocketHandshake asks the backend whether it upgrades the model
// endpoint to a WebSocket. Each path is probed three ways, because a bare 404
// on GET only says "no GET route" — it does not by itself prove "no WebSocket":
//
//	plain GET            the route with no upgrade, as a baseline
//	GET + Upgrade        what a browser WebSocket client sends
//	POST + Upgrade       what a non-browser WS client sends when it needs a body
func TestProbeWebSocketHandshake(t *testing.T) {
	requireProbe(t)
	auth, err := ReadAuth(probeHome())
	if err != nil {
		t.Fatalf("read auth: %v", err)
	}

	host := strings.TrimPrefix(DefaultBaseURL, "https://")
	for _, path := range []string{"/alpha/generate", "/alpha/agent/generate", "/alpha/sandbox/stream", "/alpha/lifecycle-events", "/alpha/ws", "/"} {
		plain, _ := rawRequest(t, host, path, auth.APIKey, http.MethodGet, false)
		getUpgrade, getHeaders := rawRequest(t, host, path, auth.APIKey, http.MethodGet, true)
		postUpgrade, postHeaders := rawRequest(t, host, path, auth.APIKey, http.MethodPost, true)
		t.Logf("%-22s plainGET=%-26s GET+Upgrade=%-26s POST+Upgrade=%-26s %s%s",
			path, plain, getUpgrade, postUpgrade, getHeaders, postHeaders)
		if strings.Contains(getUpgrade, "101") || strings.Contains(postUpgrade, "101") {
			t.Logf("RESULT: %s supports WebSocket", path)
		}
	}
}

// rawRequest performs one raw HTTP/1.1 request and returns the status line
// plus any upgrade-related response header.
func rawRequest(t *testing.T, host, path, apiKey, method string, upgrade bool) (string, string) {
	t.Helper()
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	raw, err := dialer.Dial("tcp", net.JoinHostPort(host, "443"))
	if err != nil {
		t.Fatalf("dial %s: %v", host, err)
	}
	defer raw.Close()
	conn := tls.Client(raw, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err := conn.Handshake(); err != nil {
		t.Fatalf("tls handshake: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	body := `{"config":{"workingDir":"/tmp","date":"2026-11-22","environment":"darwin","structure":[],"isGitRepo":false,"currentBranch":"","mainBranch":"","gitStatus":"","recentCommits":[]},"memory":"","taste":null,"skills":"","params":{"tools":[],"stream":true,"max_tokens":32,"system":"x","messages":[],"model":"deepseek/deepseek-v4.1-flash"},"threadId":"00000000-0000-4000-8000-000000000000"}`

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s HTTP/1.1\r\n", method, path)
	fmt.Fprintf(&b, "Host: %s\r\n", host)
	fmt.Fprintf(&b, "Authorization: Bearer %s\r\n", apiKey)
	b.WriteString("User-Agent: cli\r\n")
	fmt.Fprintf(&b, "X-Command-Code-Version: %s\r\n", probeVersion())
	b.WriteString("X-Cli-Environment: production\r\n")
	if method == http.MethodPost {
		fmt.Fprintf(&b, "Content-Type: application/json\r\nContent-Length: %d\r\n", len(body))
	}
	if upgrade {
		b.WriteString("Connection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n")
	}
	b.WriteString("\r\n")
	if method == http.MethodPost {
		b.WriteString(body)
	}
	if _, err := io.WriteString(conn, b.String()); err != nil {
		t.Fatalf("write request: %v", err)
	}

	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	status = strings.TrimSpace(status)
	var upgradeHeader string
	for i := 0; i < 30; i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "upgrade:") || strings.HasPrefix(lower, "sec-websocket-accept:") {
			upgradeHeader += line + "; "
		}
	}
	return status, upgradeHeader
}

// TestProbeThreadContinuation asks whether a stable threadId lets a follow-up
// request omit the earlier messages. If the backend keeps thread state, the
// second request (only the question) can answer with the codeword; if it does
// not, the codeword is lost and full-history resend is mandatory.
func TestProbeThreadContinuation(t *testing.T) {
	requireProbe(t)
	const codeword = "XYLOPHONE-42"
	threadID, err := newThreadID()
	if err != nil {
		t.Fatalf("newThreadID: %v", err)
	}

	first := generate(t, probeRequest(threadID,
		"Remember this codeword: "+codeword+". Reply with the single word: stored", 64))
	t.Logf("first reply: %s", compactStream(first))

	// Same threadId, no history: only the follow-up question.
	follow := generate(t, probeRequest(threadID,
		"What codeword did I ask you to remember? Reply with the codeword only.", 64))
	t.Logf("same-thread follow-up: %s", compactStream(follow))

	// Control: a fresh thread must not know it.
	other, err := newThreadID()
	if err != nil {
		t.Fatalf("newThreadID: %v", err)
	}
	control := generate(t, probeRequest(other,
		"What codeword did I ask you to remember? Reply with the codeword only.", 64))
	t.Logf("fresh-thread control: %s", compactStream(control))

	// The follow-up prompt never contains the codeword, so any occurrence in
	// its raw stream came from server-side thread state.
	followSaw := strings.Contains(follow, codeword)
	controlSaw := strings.Contains(control, codeword)
	t.Logf("raw stream contains codeword: same-thread=%v control=%v", followSaw, controlSaw)

	switch {
	case followSaw && !controlSaw:
		t.Logf("RESULT: thread continuation SUPPORTED — same threadId retains history server-side")
	case followSaw && controlSaw:
		t.Logf("RESULT: INCONCLUSIVE — codeword appeared in the control too")
	default:
		t.Logf("RESULT: thread continuation NOT supported — the follow-up on the same threadId lost the codeword")
	}
}

// compactStream reduces an SSE body to its text/reasoning deltas for readable
// logs. Command Code emits one bare JSON part per line (an optional "data: "
// prefix is tolerated), not Anthropic-style named events.
func compactStream(body string) string {
	var out strings.Builder
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimPrefix(line, "data: ")
		var ev alphaEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "text-delta", "reasoning-delta":
			if ev.Delta != "" {
				out.WriteString(ev.Delta)
			} else {
				out.WriteString(ev.Text)
			}
		}
	}
	text := strings.TrimSpace(out.String())
	if text == "" {
		if len(body) > 300 {
			body = body[:300]
		}
		return "<no delta parts> raw=" + strings.ReplaceAll(body, "\n", " ")
	}
	return text
}

// streamTiming records how a stream arrives, not just what it contains.
type streamTiming struct {
	deltas  int
	first   time.Time
	last    time.Time
	maxGap  time.Duration
	buckets map[int64]struct{}
}

// feedDelta records one delta event arrival.
func (s *streamTiming) feedDelta(now time.Time) {
	if s.deltas == 0 {
		s.first = now
	} else if gap := now.Sub(s.last); gap > s.maxGap {
		s.maxGap = gap
	}
	s.last = now
	s.deltas++
	if s.buckets == nil {
		s.buckets = map[int64]struct{}{}
	}
	s.buckets[now.UnixMilli()/50] = struct{}{}
}

func (s *streamTiming) String() string {
	span := s.last.Sub(s.first)
	return fmt.Sprintf("deltas=%d firstDelta=%s span=%s maxGap=%s burstBuckets=%d",
		s.deltas, s.first.Format("15:04:05.000"), span.Round(time.Millisecond), s.maxGap.Round(time.Millisecond), len(s.buckets))
}

// TestProbeUpstreamStreamIsIncremental proves the Command Code /alpha/generate
// stream delivers token deltas as they are produced rather than at the end.
// A buffered implementation would show one late burst and a span near zero.
func TestProbeUpstreamStreamIsIncremental(t *testing.T) {
	requireProbe(t)
	threadID, err := newThreadID()
	if err != nil {
		t.Fatalf("newThreadID: %v", err)
	}
	client := &Client{
		BaseURL: DefaultBaseURL,
		Home:    probeHome(),
		Version: probeVersion(),
		HTTP:    &http.Client{Timeout: 180 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	req := probeRequest(threadID, "Count from 1 to 60, one number per line, no other text.", 600)
	start := time.Now()
	resp, err := client.Generate(ctx, req)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		t.Fatalf("upstream status %s: %s", resp.Status, body)
	}

	var timing streamTiming
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var ev alphaEvent
		line := strings.TrimPrefix(strings.TrimSpace(scanner.Text()), "data: ")
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Type == "text-delta" || ev.Type == "reasoning-delta" {
			timing.feedDelta(time.Now())
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan stream: %v", err)
	}
	total := time.Since(start)
	t.Logf("upstream: %s total=%s", timing.String(), total.Round(time.Millisecond))
	if timing.deltas < 10 {
		t.Fatalf("expected many incremental deltas, got %d", timing.deltas)
	}
	// A buffered stream arrives in one read: near-zero span, one bucket.
	if len(timing.buckets) < 3 {
		t.Errorf("deltas arrived in %d 50ms window(s), span %s: the stream was buffered, not incremental", len(timing.buckets), timing.last.Sub(timing.first))
	}
	t.Logf("upstream streams %d deltas across %d windows (%.0f deltas/s)", timing.deltas, len(timing.buckets), float64(timing.deltas)/timing.last.Sub(timing.first).Seconds())
}

// TestProbeProxyStreamIsIncremental repeats the measurement through the running
// llm-proxy, so it covers the proxy's forwarding as well as the upstream. Point
// LLM_PROXY_URL elsewhere to test a different instance.
func TestProbeProxyStreamIsIncremental(t *testing.T) {
	requireProbe(t)
	base := os.Getenv("LLM_PROXY_URL")
	if base == "" {
		base = "http://127.0.0.1:8890"
	}
	body := `{"model":"deepseek/deepseek-v4.1-flash","max_tokens":400,"stream":true,"messages":[{"role":"user","content":"Count from 1 to 60, one number per line, no other text."}]}`
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "probe")
	req.Header.Set("anthropic-version", "2023-06-01")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		t.Fatalf("proxy status %s: %s", resp.Status, payload)
	}
	ttfb := time.Since(start)

	var timing streamTiming
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "event: ") {
			continue
		}
		// content_block_delta is the Anthropic event that carries a token;
		// message_delta only carries stop_reason/usage.
		if strings.TrimSpace(strings.TrimPrefix(line, "event: ")) == "content_block_delta" {
			timing.feedDelta(time.Now())
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan stream: %v", err)
	}
	t.Logf("proxy %s: headers=%s %s", base, ttfb.Round(time.Millisecond), timing.String())
	if timing.deltas < 10 {
		t.Fatalf("expected many content_block_delta events through the proxy, got %d", timing.deltas)
	}
	// Same burstiness check: the proxy must forward each upstream chunk, not
	// accumulate the response and write it once at the end.
	if len(timing.buckets) < 3 {
		t.Errorf("proxy delivered deltas in %d 50ms window(s), span %s: it buffered the stream", len(timing.buckets), timing.last.Sub(timing.first))
	}
	t.Logf("proxy streams %d deltas across %d windows (%.0f deltas/s)", timing.deltas, len(timing.buckets), float64(timing.deltas)/timing.last.Sub(timing.first).Seconds())
}

// TestProbeProviderAPIAvailability checks whether the documented Provider API
// (OpenAI- and Anthropic-compatible endpoints under /provider/v1) accepts this
// CLI credential, and whether it streams when it does.
func TestProbeProviderAPIAvailability(t *testing.T) {
	requireProbe(t)
	auth, err := ReadAuth(probeHome())
	if err != nil {
		t.Fatalf("read auth: %v", err)
	}
	cases := []struct {
		name string
		path string
		body string
	}{
		{
			name: "messages",
			path: "/provider/v1/messages",
			body: `{"model":"deepseek/deepseek-v4.1-flash","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"Count from 1 to 10, one number per line."}]}`,
		},
		{
			name: "chat/completions",
			path: "/provider/v1/chat/completions",
			body: `{"model":"deepseek/deepseek-v4-flash","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"Count from 1 to 10, one number per line."}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, DefaultBaseURL+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+auth.APIKey)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("x-api-key", auth.APIKey)
			start := time.Now()
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("provider request: %v", err)
			}
			defer resp.Body.Close()
			ttfb := time.Since(start)
			if resp.StatusCode != http.StatusOK {
				payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
				t.Logf("RESULT %s: status=%s ttfb=%s body=%s", tc.path, resp.Status, ttfb.Round(time.Millisecond), strings.TrimSpace(string(payload)))
				return
			}
			var events, deltas int
			scanner := bufio.NewScanner(resp.Body)
			scanner.Buffer(make([]byte, 1<<20), 1<<20)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if strings.HasPrefix(line, "event:") || strings.HasPrefix(line, "data:") {
					events++
				}
				if strings.Contains(line, `"type":"content_block_delta"`) ||
					strings.Contains(line, `"type":"text_delta"`) ||
					strings.Contains(line, "response.output_text.delta") ||
					strings.Contains(line, `"delta":{"content"`) {
					deltas++
				}
			}
			t.Logf("RESULT %s: status=200 ttfb=%s sseLines=%d textDeltas=%d total=%s", tc.path, ttfb.Round(time.Millisecond), events, deltas, time.Since(start).Round(time.Millisecond))
		})
	}
}

// TestProbeProviderModels lists the documented Provider API catalog, so the
// report can say which models answer on which endpoint (and therefore which
// upstream format a proxy could use).
func TestProbeProviderModels(t *testing.T) {
	requireProbe(t)
	auth, err := ReadAuth(probeHome())
	if err != nil {
		t.Fatalf("read auth: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DefaultBaseURL+"/provider/v1/models", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+auth.APIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("models request: %v", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("read models: %v", err)
	}
	t.Logf("models: status=%s bytes=%d", resp.Status, len(payload))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("models status %s: %s", resp.Status, payload)
	}
	var decoded struct {
		Data []struct {
			ID                 string   `json:"id"`
			SupportedEndpoints []string `json:"supported_endpoints"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode models: %v (payload %.200s)", err, payload)
	}
	responses, messages, chat := 0, 0, 0
	for _, m := range decoded.Data {
		for _, endpoint := range m.SupportedEndpoints {
			switch {
			case strings.Contains(endpoint, "/responses"):
				responses++
			case strings.Contains(endpoint, "/messages"):
				messages++
			case strings.Contains(endpoint, "/chat/completions"):
				chat++
			}
		}
	}
	t.Logf("catalog: models=%d chat=%d responses=%d messages=%d", len(decoded.Data), chat, responses, messages)
	shown := 0
	for _, m := range decoded.Data {
		if shown == 12 {
			break
		}
		if !strings.Contains(m.ID, "deepseek") && !strings.Contains(m.ID, "glm") && !strings.Contains(m.ID, "ling") {
			continue
		}
		t.Logf("  %-38s %v", m.ID, m.SupportedEndpoints)
		shown++
	}
}

// TestProbeProviderResponsesContinuation asks whether the documented Responses
// endpoint keeps server-side conversation state. If previous_response_id works,
// a client can send only the new turn instead of the whole history — the one
// mode that avoids the multi-megabyte resend.
func TestProbeProviderResponsesContinuation(t *testing.T) {
	requireProbe(t)
	const codeword = "XYLOPHONE-42"
	model := os.Getenv("COMMANDCODE_PROBE_RESPONSES_MODEL")
	if model == "" {
		model = "deepseek/deepseek-v4.1-flash"
	}
	auth, err := ReadAuth(probeHome())
	if err != nil {
		t.Fatalf("read auth: %v", err)
	}

	post := func(t *testing.T, body string) (int, []byte) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, DefaultBaseURL+"/provider/v1/responses", strings.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+auth.APIKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("responses request: %v", err)
		}
		defer resp.Body.Close()
		payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return resp.StatusCode, payload
	}

	firstBody := fmt.Sprintf(`{"model":%q,"input":%q,"store":true,"max_output_tokens":64}`,
		model, "Remember this codeword: "+codeword+". Reply with the single word: stored")
	status, payload := post(t, firstBody)
	t.Logf("first: status=%d %.220s", status, payload)
	if status != http.StatusOK {
		t.Fatalf("first response failed: %d", status)
	}
	var first struct {
		ID     string `json:"id"`
		Stored *bool  `json:"store"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(payload, &first); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	if first.ID == "" {
		t.Fatalf("no response id to continue from: %.300s", payload)
	}
	t.Logf("first response id=%s stored=%v", first.ID, first.Stored)

	followBody := fmt.Sprintf(`{"model":%q,"input":%q,"store":true,"previous_response_id":%q,"max_output_tokens":64}`,
		model, "What codeword did I ask you to remember? Reply with the codeword only.", first.ID)
	status, follow := post(t, followBody)
	t.Logf("follow-up: status=%d %.300s", status, follow)

	controlBody := fmt.Sprintf(`{"model":%q,"input":%q,"store":true,"max_output_tokens":64}`,
		model, "What codeword did I ask you to remember? Reply with the codeword only.")
	_, control := post(t, controlBody)

	followSaw := strings.Contains(string(follow), codeword)
	controlSaw := strings.Contains(string(control), codeword)
	t.Logf("codeword in response: continuation=%v control=%v", followSaw, controlSaw)
	switch {
	case followSaw && !controlSaw:
		t.Logf("RESULT: previous_response_id WORKS — the endpoint keeps server-side state; history need not be resent")
	case status != http.StatusOK:
		t.Logf("RESULT: previous_response_id REFUSED (status %d) — the endpoint is stateless for this model", status)
	default:
		t.Logf("RESULT: previous_response_id accepted but did not retain state — treat as stateless")
	}
}
