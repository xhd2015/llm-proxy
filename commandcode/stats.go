package commandcode

import (
	"net"
	"net/http"
	"time"
)

// defaultLiveSessionFor bounds the sessions the stats endpoint counts.
const defaultLiveSessionFor = 10 * time.Minute

// windowStatus is one quota window in the stats payload.
type windowStatus struct {
	Used     float64 `json:"used"`
	Cap      float64 `json:"cap"`
	ResetAt  string  `json:"resetAt,omitempty"`
	Exceeded bool    `json:"exceeded,omitempty"`
}

// accountStatus is one credential's live row.
type accountStatus struct {
	Name        string       `json:"name"`
	Home        string       `json:"home,omitempty"`
	Available   bool         `json:"available"`
	Reason      string       `json:"reason,omitempty"`
	InFlight    int          `json:"inFlight"`
	Sessions    int          `json:"sessions"`
	FiveHour    windowStatus `json:"fiveHour"`
	Weekly      windowStatus `json:"weekly"`
	RefreshedAt string       `json:"refreshedAt,omitempty"`
	LastError   string       `json:"lastError,omitempty"`
}

// accountsResponse is GET /v1/accounts.
type accountsResponse struct {
	Accounts []accountStatus `json:"accounts"`
	// LiveSessionForSeconds tells the caller which "live" window the counts use.
	LiveSessionForSeconds int `json:"liveSessionForSeconds"`
}

// accounts reports the single-pool view for the standalone proxy.
func (h *handler) accounts(w http.ResponseWriter, r *http.Request) {
	var pools []*AccountPool
	if h.pool != nil {
		pools = append(pools, h.pool)
	}
	ServeAccounts(w, r, pools, h.liveSessionWindow())
}

// ServeAccounts writes per-credential quota, in-flight requests, and live
// sessions. eluc's AI usage page reads it, and so can any local operator. It is
// the multi-pool entry the config-mode proxy mounts at /v1/accounts, where one
// provider may own several pools.
func ServeAccounts(w http.ResponseWriter, r *http.Request, pools []*AccountPool, liveFor time.Duration) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Local only: this view names credentials and their quota, so a proxy that
	// was bound to a public interface must not serve it.
	if !isLoopbackRemote(r) {
		http.NotFound(w, r)
		return
	}
	if liveFor <= 0 {
		liveFor = defaultLiveSessionFor
	}
	response := accountsResponse{LiveSessionForSeconds: int(liveFor.Seconds())}
	for _, pool := range pools {
		if pool == nil {
			continue
		}
		counts := map[string]int{}
		for _, session := range pool.Registry().Snapshot(liveFor) {
			if session.Account != "" {
				counts[session.Account]++
			}
		}
		for _, stat := range pool.Snapshot() {
			response.Accounts = append(response.Accounts, accountStatus{
				Name:        stat.Name,
				Home:        stat.Home,
				Available:   stat.Available,
				Reason:      stat.Reason,
				InFlight:    stat.InFlight,
				Sessions:    counts[stat.Name],
				FiveHour:    windowStatusOf(stat.FiveHour),
				Weekly:      windowStatusOf(stat.Weekly),
				RefreshedAt: formatStamp(stat.RefreshedAt),
				LastError:   stat.LastError,
			})
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *handler) liveSessionWindow() time.Duration {
	if h.opts.LiveSessionFor > 0 {
		return h.opts.LiveSessionFor
	}
	return defaultLiveSessionFor
}

func windowStatusOf(window WindowUsage) windowStatus {
	return windowStatus{
		Used:     window.Used,
		Cap:      window.Cap,
		ResetAt:  formatStamp(window.ResetAt),
		Exceeded: window.Exceeded,
	}
}

func formatStamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func isLoopbackRemote(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
