package openai

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/xhd2015/dot-pkgs/go-pkgs/shell/open"
	"github.com/xhd2015/less-gen/flags"
	"golang.org/x/term"
)

//go:embed config_web/*
var configWebAssets embed.FS

const configWebHelp = `
Usage: llm-proxy web --config FILE
       llm-proxy --config FILE web

Edit the selected JSON config and preview DSH, Codex, and Grok exports in a local browser.
The server binds to 127.0.0.1 on an automatic port. Ctrl+C stops it.
No authentication is required; local processes can access the selected config.
Save validates, checks for external changes, and backs up the old file.
Neither the running proxy nor DSH settings are reloaded or modified.

Options:
  --config FILE  configuration file to edit (must exist)
  -h, --help     show this help
`

func handleConfigWeb(path string, args []string) error {
	args, err := flags.String("--config", &path).Help("-h,--help", configWebHelp).Parse(args)
	if err != nil {
		return err
	}
	if len(args) != 0 {
		return fmt.Errorf("unrecognized extra args: %s", strings.Join(args, " "))
	}
	if path == "" {
		return fmt.Errorf("web requires --config FILE")
	}
	store, err := newConfigWebStore(path)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	origin := "http://" + listener.Addr().String()
	server := &http.Server{Handler: newConfigWebHandler(store, listener.Addr().String()), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer server.Close()
	url := origin + "/"
	label := "Editor:"
	if term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == "" {
		label = "\033[32mEditor:\033[0m"
	}
	fmt.Fprintf(os.Stdout, "%s %s\nConfig: %s\nPress Ctrl+C to stop.\n", label, url, store.path)
	// Browser launch is independent of serving and shutdown; a desktop opener may block.
	go func() {
		if _, err := open.URL(url); err != nil {
			label := "warning:"
			if stderrColorEnabled(ColorAuto) {
				label = "\033[33mwarning:\033[0m"
			}
			fmt.Fprintf(os.Stderr, "%s could not open browser; open the printed URL manually.\n", label)
		}
	}()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

type configWebDiagnostic struct {
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Message  string `json:"message"`
}

type configWebReport struct {
	Valid       bool                        `json:"valid"`
	Diagnostics []configWebDiagnostic       `json:"diagnostics"`
	Previews    map[string]configWebPreview `json:"previews"`
}

type configWebPreview struct {
	Content string `json:"content"`
	Format  string `json:"format"`
	Error   string `json:"error,omitempty"`
}

func inspectConfigWebDraft(text string) configWebReport {
	config, routes, diagnostics := inspectProxyConfigBytes([]byte(text))
	report := configWebReport{Valid: diagnosticErrors(diagnostics) == nil, Diagnostics: []configWebDiagnostic{}, Previews: map[string]configWebPreview{}}
	if report.Valid {
		for _, runner := range []string{"dsh", "codex", "grok"} {
			preview := configWebPreview{Format: "toml"}
			if runner == "dsh" {
				preview.Format = "yaml"
			}
			content, err := generateConfigModels(config.Listen, routes, runner)
			if err != nil {
				preview.Error = err.Error()
				diagnostics = append(diagnostics, configDiagnostic{"warning", "models", runner + " export: " + err.Error()})
			} else {
				preview.Content = content
			}
			report.Previews[runner] = preview
		}
	}
	for _, d := range diagnostics {
		report.Diagnostics = append(report.Diagnostics, configWebDiagnostic{d.severity, d.path, d.message})
	}
	return report
}

func newConfigWebHandler(store *configWebStore, host string) http.Handler {
	assets, _ := fs.Sub(configWebAssets, "config_web")
	static := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.Host != host || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+host) {
			http.Error(w, "forbidden origin or host", http.StatusForbidden)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "method not allowed", 405)
				return
			}
			if r.URL.Path != "/" && r.URL.Path != "/app.js" && r.URL.Path != "/tree.mjs" && r.URL.Path != "/style.css" {
				http.NotFound(w, r)
				return
			}
			static.ServeHTTP(w, r)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "cross-origin API access is forbidden", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/api/config" && r.Method == http.MethodGet {
			text, revision, err := store.read()
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			writeConfigWebJSON(w, 200, map[string]any{"text": text, "revision": revision, "path": store.path, "report": inspectConfigWebDraft(text)})
			return
		}
		if r.URL.Path != "/api/validate" && r.URL.Path != "/api/save" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "expected application/json", 415)
			return
		}
		var draft struct {
			Text     string `json:"text"`
			Revision string `json:"revision"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, configWebMaxBytes*2))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&draft); err != nil {
			http.Error(w, "invalid editor request", 400)
			return
		}
		if decoder.Decode(new(any)) != io.EOF || len(draft.Text) > configWebMaxBytes {
			http.Error(w, "invalid or oversized editor request", 400)
			return
		}
		report := inspectConfigWebDraft(draft.Text)
		if r.URL.Path == "/api/validate" {
			writeConfigWebJSON(w, 200, report)
			return
		}
		if !report.Valid {
			writeConfigWebJSON(w, 422, report)
			return
		}
		revision, backup, err := store.save(draft.Text, draft.Revision)
		if err != nil {
			status := 500
			if errors.Is(err, errConfigWebChanged) {
				status = 409
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeConfigWebJSON(w, 200, map[string]any{"revision": revision, "backup": backup, "report": report})
	})
}

func writeConfigWebJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
