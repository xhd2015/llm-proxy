package commandcode

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// uuidRe matches the v4 UUID shape Command Code accepts as a threadId.
var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// Digest is one link of a conversation's fingerprint chain.
type Digest [32]byte

func (d Digest) String() string { return hex.EncodeToString(d[:]) }

// Field separators. Content is JSON text, so control bytes cannot collide with
// a field boundary.
const (
	fieldSep  = 0x00
	blockSep  = 0x1e
	recordSep = 0x1f
)

// Turn is one user turn: the messages that led up to it plus its user text.
// The chain hashes the previous digest, this body, then the user text.
type Turn struct {
	body     []byte
	userText string
}

// SplitTurns groups messages into user turns. A turn starts at a user message
// that carries at least one text block; its body is everything between the
// previous user text and this one, so tool calls and tool results stay inside
// the turn they belong to. Messages after the last user text are not part of
// any turn: they grow while an agent works through tool calls and must not
// move the fingerprint.
func SplitTurns(messages []alphaMessage) []Turn {
	var turns []Turn
	var body []byte
	for _, message := range messages {
		userText, others := splitUserBlocks(message)
		if userText != "" {
			body = appendCanonicalBlocks(body, others)
			turns = append(turns, Turn{body: body, userText: userText})
			body = nil
			continue
		}
		body = appendCanonicalMessage(body, message)
	}
	return turns
}

// splitUserBlocks separates a user message's text (a turn boundary) from its
// other blocks (tool results belong to the previous turn's body).
func splitUserBlocks(message alphaMessage) (string, []alphaBlock) {
	if message.Role != "user" {
		return "", nil
	}
	var texts []string
	others := make([]alphaBlock, 0, len(message.Content))
	for _, block := range message.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			texts = append(texts, block.Text)
			continue
		}
		others = append(others, block)
	}
	return strings.Join(texts, "\n"), others
}

func appendCanonicalBlocks(dst []byte, blocks []alphaBlock) []byte {
	for _, block := range blocks {
		dst = appendCanonicalBlock(dst, block)
	}
	return dst
}

func appendCanonicalMessage(dst []byte, message alphaMessage) []byte {
	dst = append(dst, message.Role...)
	dst = append(dst, fieldSep)
	for _, block := range message.Content {
		dst = appendCanonicalBlock(dst, block)
	}
	dst = append(dst, blockSep)
	return dst
}

// appendCanonicalBlock writes one content block. Input and output payloads are
// raw JSON, which is already canonical enough for a fingerprint.
func appendCanonicalBlock(dst []byte, block alphaBlock) []byte {
	dst = append(dst, block.Type...)
	dst = append(dst, recordSep)
	switch block.Type {
	case "text", "reasoning":
		dst = append(dst, block.Text...)
	case "tool-call":
		dst = append(dst, block.ToolCallID...)
		dst = append(dst, fieldSep)
		dst = append(dst, block.ToolName...)
		dst = append(dst, fieldSep)
		dst = append(dst, block.Input...)
	case "tool-result":
		dst = append(dst, block.ToolCallID...)
		dst = append(dst, fieldSep)
		if block.Output != nil {
			dst = append(dst, block.Output.Value...)
		}
	case "image":
		dst = append(dst, block.MimeType...)
		dst = append(dst, fieldSep)
		dst = append(dst, block.Image...)
	default:
		dst = append(dst, block.Text...)
	}
	dst = append(dst, blockSep)
	return dst
}

// ChainDigests derives the conversation's fingerprint chain: one digest per
// user turn, each folded over the previous one, so an appended turn extends the
// chain instead of replacing it.
//
//	sha0  = H(model ‖ system ‖ body0 ‖ user0)
//	sha_i = H(sha_{i-1} ‖ body_i ‖ user_i)
//
// A request with no user text at all falls back to a digest of everything, so
// two different histories can never collapse onto one key.
func ChainDigests(model string, system any, messages []alphaMessage) []Digest {
	turns := SplitTurns(messages)
	if len(turns) == 0 {
		h := sha256.New()
		writeSystem(h, model, system)
		for _, message := range messages {
			writeCanonicalTo(h, message)
		}
		var only Digest
		copy(only[:], h.Sum(nil))
		return []Digest{only}
	}

	first := sha256.New()
	writeSystem(first, model, system)
	first.Write(turns[0].body)
	io.WriteString(first, turns[0].userText)
	var previous Digest
	copy(previous[:], first.Sum(nil))

	chain := make([]Digest, 0, len(turns))
	chain = append(chain, previous)
	for _, turn := range turns[1:] {
		h := sha256.New()
		h.Write(previous[:])
		h.Write(turn.body)
		io.WriteString(h, turn.userText)
		copy(previous[:], h.Sum(nil))
		chain = append(chain, previous)
	}
	return chain
}

func writeSystem(h hash.Hash, model string, system any) {
	io.WriteString(h, model)
	h.Write([]byte{fieldSep})
	switch value := system.(type) {
	case nil:
	case string:
		io.WriteString(h, value)
	case []alphaBlock:
		for _, block := range value {
			h.Write(appendCanonicalBlock(nil, block))
		}
	default:
		fmt.Fprintf(h, "%v", value)
	}
	h.Write([]byte{fieldSep})
}

func writeCanonicalTo(h hash.Hash, message alphaMessage) {
	h.Write(appendCanonicalMessage(nil, message))
}

// Session is one conversation the proxy has routed.
type Session struct {
	// ID is the threadId sent upstream. It is allocated, not content-derived.
	ID string
	// Account is the credential currently serving the conversation.
	Account string

	base     int // absolute index of shas[0]
	shas     []Digest
	lastSeen time.Time
	inFlight int
}

// LastSeen reports when the session last carried a request.
func (s *Session) LastSeen() time.Time { return s.lastSeen }

// InFlight reports the requests currently streaming for this session.
func (s *Session) InFlight() int { return s.inFlight }

// MatchInfo describes how a chain resolved to a session.
type MatchInfo struct {
	// Created is true when a new session id was allocated.
	Created bool
	// Forked is true when the chain shared a prefix with an existing session
	// but diverged, so it is a branch rather than a continuation.
	Forked bool
	// Parent is the longest-prefix session when Forked.
	Parent *Session
	// Matched is how many retained digests lined up.
	Matched int
}

// Registry maps fingerprint chains to sessions. It is the only mutable routing
// state: one lock covers match, append, and account binding, so two concurrent
// first requests of the same conversation cannot create two sessions.
type Registry struct {
	mu       sync.Mutex
	sessions map[string]*Session
	byTip    map[Digest][]*Session
	window   int
	max      int
	ttl      time.Duration
	now      func() time.Time
	newID    func() (string, error)
	lastGC   time.Time
}

// RegistryOptions configures a Registry.
type RegistryOptions struct {
	// Window is how many recent digests each session remembers (default 10).
	Window int
	// Max caps the number of sessions (default 2000).
	Max int
	// TTL drops sessions idle longer than this (default 60m).
	TTL time.Duration
	Now func() time.Time
	// NewID allocates a session id; it defaults to a random UUID.
	NewID func() (string, error)
}

const (
	defaultSessionWindow = 10
	defaultSessionMax    = 2000
	defaultSessionTTL    = 60 * time.Minute
	gcEvery              = time.Minute
)

// NewRegistry builds an empty registry.
func NewRegistry(options RegistryOptions) *Registry {
	if options.Window <= 0 {
		options.Window = defaultSessionWindow
	}
	if options.Max <= 0 {
		options.Max = defaultSessionMax
	}
	if options.TTL <= 0 {
		options.TTL = defaultSessionTTL
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewID == nil {
		options.NewID = newThreadID
	}
	return &Registry{
		sessions: map[string]*Session{},
		byTip:    map[Digest][]*Session{},
		window:   options.Window,
		max:      options.Max,
		ttl:      options.TTL,
		now:      options.Now,
		newID:    options.NewID,
	}
}

// MatchOrCreate resolves a chain to a session. The chain is computed outside
// the lock; everything that mutates state happens inside it.
func (r *Registry) MatchOrCreate(chain []Digest) (*Session, MatchInfo) {
	if len(chain) == 0 {
		return nil, MatchInfo{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.gcLocked(now)

	best, matched := r.longestLocked(chain)
	if best != nil && matched == len(best.shas) {
		// Continuation: append only the turns this request adds.
		next := best.base + len(best.shas)
		r.unindexLocked(best)
		for index := next; index < len(chain); index++ {
			best.shas = append(best.shas, chain[index])
		}
		r.trimLocked(best)
		r.indexLocked(best)
		best.lastSeen = now
		return best, MatchInfo{Matched: matched}
	}

	info := MatchInfo{Matched: matched}
	if best != nil {
		info.Forked = true
		info.Parent = best
	}
	id, err := r.newID()
	if err != nil || id == "" {
		return nil, info
	}
	session := &Session{ID: id, base: 0, shas: append([]Digest(nil), chain...), lastSeen: now}
	r.trimLocked(session)
	r.sessions[id] = session
	r.indexLocked(session)
	info.Created = true
	r.evictLocked()
	return session, info
}

// longestLocked returns the session sharing the longest prefix with chain,
// aligned on its retained window.
func (r *Registry) longestLocked(chain []Digest) (*Session, int) {
	var best *Session
	bestLen := 0
	consider := func(session *Session) {
		matched := 0
		for matched < len(session.shas) {
			index := session.base + matched
			if index >= len(chain) || chain[index] != session.shas[matched] {
				break
			}
			matched++
		}
		if matched > bestLen || (matched == bestLen && matched > 0 && session.lastSeen.After(best.lastSeen)) {
			best, bestLen = session, matched
		}
	}
	// Fast path: a continuation shares the tip.
	for _, session := range r.byTip[chain[len(chain)-1]] {
		consider(session)
	}
	if best != nil && bestLen == len(best.shas) {
		return best, bestLen
	}
	for _, session := range r.sessions {
		consider(session)
	}
	return best, bestLen
}

// AccountOf reports the credential currently bound to a session.
func (r *Registry) AccountOf(sessionID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if session, ok := r.sessions[sessionID]; ok {
		return session.Account
	}
	return ""
}

// SetAccount binds a session to the credential that serves it.
func (r *Registry) SetAccount(sessionID, account string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if session, ok := r.sessions[sessionID]; ok {
		session.Account = account
	}
}

// Acquire marks one in-flight request on the session.
func (r *Registry) Acquire(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if session, ok := r.sessions[sessionID]; ok {
		session.inFlight++
		session.lastSeen = r.now()
	}
}

// Release clears one in-flight request.
func (r *Registry) Release(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if session, ok := r.sessions[sessionID]; ok && session.inFlight > 0 {
		session.inFlight--
		session.lastSeen = r.now()
	}
}

// SessionStat is one row of the live session view.
type SessionStat struct {
	ID       string
	Account  string
	InFlight int
	LastSeen time.Time
}

// Snapshot lists sessions seen within liveFor.
func (r *Registry) Snapshot(liveFor time.Duration) []SessionStat {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	stats := make([]SessionStat, 0, len(r.sessions))
	for _, session := range r.sessions {
		if liveFor > 0 && now.Sub(session.lastSeen) > liveFor {
			continue
		}
		stats = append(stats, SessionStat{
			ID:       session.ID,
			Account:  session.Account,
			InFlight: session.inFlight,
			LastSeen: session.lastSeen,
		})
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].LastSeen.After(stats[j].LastSeen) })
	return stats
}

func (r *Registry) trimLocked(session *Session) {
	if len(session.shas) <= r.window {
		return
	}
	drop := len(session.shas) - r.window
	session.shas = append([]Digest(nil), session.shas[drop:]...)
	session.base += drop
}

func (r *Registry) indexLocked(session *Session) {
	if len(session.shas) == 0 {
		return
	}
	tip := session.shas[len(session.shas)-1]
	r.byTip[tip] = append(r.byTip[tip], session)
}

func (r *Registry) unindexLocked(session *Session) {
	if len(session.shas) == 0 {
		return
	}
	tip := session.shas[len(session.shas)-1]
	entries := r.byTip[tip]
	for i, entry := range entries {
		if entry == session {
			r.byTip[tip] = append(entries[:i], entries[i+1:]...)
			break
		}
	}
	if len(r.byTip[tip]) == 0 {
		delete(r.byTip, tip)
	}
}

// evictLocked drops the least recently seen session when the registry is full.
func (r *Registry) evictLocked() {
	for len(r.sessions) > r.max {
		var oldest *Session
		for _, session := range r.sessions {
			if oldest == nil || session.lastSeen.Before(oldest.lastSeen) {
				oldest = session
			}
		}
		if oldest == nil {
			return
		}
		r.removeLocked(oldest)
	}
}

func (r *Registry) removeLocked(session *Session) {
	r.unindexLocked(session)
	delete(r.sessions, session.ID)
}

// gcLocked drops idle sessions at most once a minute.
func (r *Registry) gcLocked(now time.Time) {
	if !r.lastGC.IsZero() && now.Sub(r.lastGC) < gcEvery {
		return
	}
	r.lastGC = now
	for _, session := range r.sessions {
		if now.Sub(session.lastSeen) > r.ttl {
			r.removeLocked(session)
		}
	}
}

// Len reports the number of tracked sessions.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}

// String renders a digest for logs.
func (d Digest) Short() string { return hex.EncodeToString(d[:4]) }
