package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// AdminPrefix is the control-plane surface mounted on the same listener as the
// proxy routes. The leading underscore keeps it out of the /v1 namespace and
// makes an accidental public exposure obvious in logs.
const AdminPrefix = "/_llmproxy"

const adminMaxBody = 1 << 16

type adminRequest struct {
	Provider   string `json:"provider"`
	Credential string `json:"credential"`
}

func (h *configHandler) serveAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, AdminPrefix) {
		return false
	}
	route := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, AdminPrefix), "/")
	switch route {
	case "/status":
		if !adminMethod(w, r, http.MethodGet) {
			return true
		}
		writeAdminJSON(w, http.StatusOK, h.Status())
		return true
	case "/reload":
		if !adminMethod(w, r, http.MethodPost) {
			return true
		}
		if !adminGuarded(w, r) {
			return true
		}
		report, err := h.Reload()
		writeAdminResult(w, report, err)
		return true
	case "/accounts/select":
		if !adminMethod(w, r, http.MethodPost) {
			return true
		}
		if !adminGuarded(w, r) {
			return true
		}
		request, ok := readAdminRequest(w, r)
		if !ok {
			return true
		}
		report, err := h.SelectCredential(request.Provider, request.Credential)
		writeAdminResult(w, report, err)
		return true
	case "/model-listing/refresh":
		if !adminMethod(w, r, http.MethodPost) {
			return true
		}
		if !adminGuarded(w, r) {
			return true
		}
		h.wakeModelListingLoop()
		writeAdminJSON(w, http.StatusAccepted, map[string]any{
			"ok":      true,
			"message": "model listing refresh scheduled",
		})
		return true
	}
	writeAdminError(w, http.StatusNotFound, fmt.Sprintf("unknown control endpoint %q", r.URL.Path))
	return true
}

func adminMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeAdminError(w, http.StatusMethodNotAllowed, fmt.Sprintf("method %s not allowed; use %s", r.Method, method))
	return false
}

// adminGuarded rejects cross-site and non-JSON mutations. The proxy binds
// loopback, but a browser tab on any site can still reach it; the supervisor
// (eluc) forwards these calls with Origin stripped and sends JSON.
func adminGuarded(w http.ResponseWriter, r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		writeAdminError(w, http.StatusForbidden, "cross-site control requests are not allowed")
		return false
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(parsed.Host, r.Host) {
			writeAdminError(w, http.StatusForbidden, fmt.Sprintf("origin %q is not this server", origin))
			return false
		}
	}
	mediaType := strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0])
	if mediaType != "" && !strings.EqualFold(mediaType, "application/json") {
		writeAdminError(w, http.StatusUnsupportedMediaType, "control requests must be application/json")
		return false
	}
	return true
}

func readAdminRequest(w http.ResponseWriter, r *http.Request) (adminRequest, bool) {
	var request adminRequest
	body := http.MaxBytesReader(w, r.Body, adminMaxBody)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeAdminError(w, http.StatusBadRequest, "control request must be a JSON object: "+err.Error())
		return adminRequest{}, false
	}
	request.Provider = strings.TrimSpace(request.Provider)
	request.Credential = strings.TrimSpace(request.Credential)
	if request.Provider == "" || request.Credential == "" {
		writeAdminError(w, http.StatusBadRequest, "provider and credential are required")
		return adminRequest{}, false
	}
	return request, true
}

func writeAdminResult(w http.ResponseWriter, report any, err error) {
	if err != nil {
		writeAdminError(w, adminStatusOf(err), err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, report)
}

func writeAdminJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeAdminError(w http.ResponseWriter, status int, message string) {
	writeAdminJSON(w, status, map[string]any{"ok": false, "error": message})
}

// adminStatusOfError is exported through the embeddable API so a supervisor can
// map a failed mutation onto its own HTTP status without string matching.
func IsAdminError(err error) (*AdminError, bool) {
	var failure *AdminError
	if errors.As(err, &failure) {
		return failure, true
	}
	return nil, false
}
