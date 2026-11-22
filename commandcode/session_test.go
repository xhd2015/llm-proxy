package commandcode

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func userText(text string) alphaMessage {
	return alphaMessage{Role: "user", Content: []alphaBlock{{Type: "text", Text: text}}}
}

func assistantText(text string) alphaMessage {
	return alphaMessage{Role: "assistant", Content: []alphaBlock{{Type: "text", Text: text}}}
}

func toolUse(id, name string) alphaMessage {
	return alphaMessage{Role: "assistant", Content: []alphaBlock{{Type: "tool-call", ToolCallID: id, ToolName: name, Input: []byte(`{}`)}}}
}

func toolResult(id, value string) alphaMessage {
	return alphaMessage{Role: "user", Content: []alphaBlock{{Type: "tool-result", ToolCallID: id, Output: &alphaToolOutput{Type: "text", Value: value}}}}
}

func conversation(turns ...string) []alphaMessage {
	var messages []alphaMessage
	for i, text := range turns {
		if i > 0 {
			messages = append(messages, assistantText(fmt.Sprintf("reply %d", i)))
		}
		messages = append(messages, userText(text))
	}
	return messages
}

func chainOf(turns ...string) []Digest {
	return ChainDigests("model", "system", conversation(turns...))
}

func TestSplitTurnsTreatsToolResultsAsBody(t *testing.T) {
	messages := []alphaMessage{
		userText("first"),
		assistantText("working"),
		toolUse("call-1", "bash"),
		toolResult("call-1", "output"),
		assistantText("done"),
		userText("second"),
	}
	turns := SplitTurns(messages)
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(turns))
	}
	if turns[0].userText != "first" || turns[1].userText != "second" {
		t.Fatalf("unexpected turn texts: %q, %q", turns[0].userText, turns[1].userText)
	}
	if len(turns[0].body) != 0 {
		t.Fatalf("first turn body should be empty, got %d bytes", len(turns[0].body))
	}
	if len(turns[1].body) == 0 {
		t.Fatal("second turn body must carry the tool traffic")
	}
}

func TestChainIgnoresTrailingToolTraffic(t *testing.T) {
	base := conversation("hello", "next")
	withTools := append(append([]alphaMessage{}, base...), assistantText("working"), toolUse("c1", "bash"), toolResult("c1", "ok"))

	if a, b := ChainDigests("m", "s", base), ChainDigests("m", "s", withTools); len(a) != len(b) || a[len(a)-1] != b[len(b)-1] {
		t.Fatalf("trailing tool traffic moved the fingerprint chain: %v vs %v", a, b)
	}
}

func TestChainExtendsByOnePerTurn(t *testing.T) {
	one := chainOf("hello")
	two := chainOf("hello", "next")
	three := chainOf("hello", "next", "again")

	if len(one) != 1 || len(two) != 2 || len(three) != 3 {
		t.Fatalf("chain lengths = %d/%d/%d, want 1/2/3", len(one), len(two), len(three))
	}
	if one[0] != two[0] || two[0] != three[0] || two[1] != three[1] {
		t.Fatal("appending a turn must not rewrite earlier digests")
	}
}

func TestChainDivergesOnFork(t *testing.T) {
	parent := chainOf("hello", "next")
	fork := chainOf("hello", "different")

	if parent[0] != fork[0] {
		t.Fatal("a fork must share the prefix")
	}
	if parent[1] == fork[1] {
		t.Fatal("a fork must diverge after the shared prefix")
	}
}

func TestChainWithoutUserTextStillDistinguishesHistories(t *testing.T) {
	a := ChainDigests("m", "s", []alphaMessage{assistantText("one")})
	b := ChainDigests("m", "s", []alphaMessage{assistantText("two")})
	if a[0] == b[0] {
		t.Fatal("histories without user text collapsed onto one digest")
	}
}

func TestChainDependsOnModelAndSystem(t *testing.T) {
	messages := conversation("hello")
	base := ChainDigests("m1", "sys", messages)
	if ChainDigests("m2", "sys", messages)[0] == base[0] {
		t.Fatal("model must affect the digest")
	}
	if ChainDigests("m1", "sys2", messages)[0] == base[0] {
		t.Fatal("system prompt must affect the digest")
	}
}

func TestRegistryContinuesAndExtends(t *testing.T) {
	registry := NewRegistry(RegistryOptions{})
	first, info := registry.MatchOrCreate(chainOf("hello"))
	if first == nil || !info.Created {
		t.Fatalf("first match: %+v info=%+v", first, info)
	}

	again, info := registry.MatchOrCreate(chainOf("hello"))
	if again.ID != first.ID || info.Created {
		t.Fatalf("repeat request created a new session: %s vs %s (%+v)", again.ID, first.ID, info)
	}

	grown, info := registry.MatchOrCreate(chainOf("hello", "next"))
	if grown.ID != first.ID || info.Created {
		t.Fatalf("appended turn created a new session: %s (%+v)", grown.ID, info)
	}
}

func TestRegistryForkGetsNewID(t *testing.T) {
	registry := NewRegistry(RegistryOptions{})
	parent, _ := registry.MatchOrCreate(chainOf("hello", "next"))

	fork, info := registry.MatchOrCreate(chainOf("hello", "different"))
	if fork.ID == parent.ID {
		t.Fatal("a fork reused the parent session id")
	}
	if !info.Created || !info.Forked || info.Parent == nil || info.Parent.ID != parent.ID {
		t.Fatalf("fork info = %+v", info)
	}
	// The parent keeps serving its own chain.
	if again, _ := registry.MatchOrCreate(chainOf("hello", "next")); again.ID != parent.ID {
		t.Fatalf("parent session was disturbed by the fork: %s", again.ID)
	}
}

func TestRegistryWindowSlidesPastTenTurns(t *testing.T) {
	registry := NewRegistry(RegistryOptions{Window: 10})
	turns := make([]string, 0, 20)
	var id string
	for i := range 20 {
		turns = append(turns, fmt.Sprintf("turn-%02d", i))
		session, info := registry.MatchOrCreate(chainOf(turns...))
		if session == nil {
			t.Fatalf("turn %d: no session (%+v)", i, info)
		}
		if i == 0 {
			id = session.ID
			continue
		}
		if info.Created {
			t.Fatalf("turn %d created a new session; the window lost the match", i)
		}
		if session.ID != id {
			t.Fatalf("turn %d changed session: %s != %s", i, session.ID, id)
		}
	}
	if registry.Len() != 1 {
		t.Fatalf("sessions = %d, want 1", registry.Len())
	}
}

func TestRegistryRewindStartsNewSession(t *testing.T) {
	registry := NewRegistry(RegistryOptions{})
	registry.MatchOrCreate(chainOf("a", "b", "c"))
	short, info := registry.MatchOrCreate(chainOf("a"))
	if !info.Created {
		t.Fatalf("a shorter chain must not continue the longer session: %+v", info)
	}
	if !info.Forked || info.Matched != 1 {
		t.Fatalf("rewind info = %+v, want fork with 1 matched", info)
	}
	if short.ID == "" {
		t.Fatal("no session for the rewound chain")
	}
}

func TestRegistryConcurrentFirstRequests(t *testing.T) {
	registry := NewRegistry(RegistryOptions{})
	chain := chainOf("hello")
	var wg sync.WaitGroup
	ids := make([]string, 16)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session, _ := registry.MatchOrCreate(chain)
			if session != nil {
				ids[i] = session.ID
			}
		}()
	}
	wg.Wait()
	for i, id := range ids {
		if id == "" {
			t.Fatalf("goroutine %d got no session", i)
		}
		if id != ids[0] {
			t.Fatalf("concurrent first requests created %s and %s", ids[0], id)
		}
	}
	if registry.Len() != 1 {
		t.Fatalf("sessions = %d, want 1", registry.Len())
	}
}

func TestRegistryEvictsLeastRecentlySeen(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	registry := NewRegistry(RegistryOptions{Max: 2, Now: clock.Now})
	registry.MatchOrCreate(chainOf("a"))
	clock.advance(time.Second)
	registry.MatchOrCreate(chainOf("b"))
	clock.advance(time.Second)
	registry.MatchOrCreate(chainOf("c"))

	if registry.Len() != 2 {
		t.Fatalf("sessions = %d, want 2", registry.Len())
	}
	if session, _ := registry.MatchOrCreate(chainOf("a")); session == nil {
		t.Fatal("evicted session could not be recreated")
	}
	if registry.Len() != 2 {
		t.Fatalf("sessions = %d after recreation, want 2", registry.Len())
	}
}

func TestRegistryDropsIdleSessions(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	registry := NewRegistry(RegistryOptions{TTL: 10 * time.Minute, Now: clock.Now})
	session, _ := registry.MatchOrCreate(chainOf("old"))
	clock.advance(11 * time.Minute)
	// Any new request sweeps; the idle session must be gone.
	registry.MatchOrCreate(chainOf("new"))
	if registry.Len() != 1 {
		t.Fatalf("sessions = %d, want 1 after TTL sweep", registry.Len())
	}
	if stats := registry.Snapshot(0); len(stats) != 1 || stats[0].ID == session.ID {
		t.Fatalf("snapshot = %+v", stats)
	}
}

func TestRegistryAccountAndInFlight(t *testing.T) {
	registry := NewRegistry(RegistryOptions{})
	session, _ := registry.MatchOrCreate(chainOf("hello"))
	registry.SetAccount(session.ID, "xhd2015")
	registry.Acquire(session.ID)
	registry.Acquire(session.ID)

	stats := registry.Snapshot(time.Hour)
	if len(stats) != 1 {
		t.Fatalf("snapshot = %+v", stats)
	}
	if stats[0].Account != "xhd2015" || stats[0].InFlight != 2 {
		t.Fatalf("stat = %+v", stats[0])
	}
	registry.Release(session.ID)
	if stats := registry.Snapshot(time.Hour); stats[0].InFlight != 1 {
		t.Fatalf("inFlight = %d after release", stats[0].InFlight)
	}
	registry.Release(session.ID)
	registry.Release(session.ID) // extra release must not go negative
	if stats := registry.Snapshot(time.Hour); stats[0].InFlight != 0 {
		t.Fatalf("inFlight = %d after releases", stats[0].InFlight)
	}
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
