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
