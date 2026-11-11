package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	logutil "github.com/xhd2015/llm-proxy/log"
)

// EmbedOptions configures an in-process proxy. Embedding keeps the proxy's
// routing, model listing, and control endpoints, but the host process owns the
// listener, so there is no listen address to reconcile.
type EmbedOptions struct {
	// ConfigPath is the llm-proxy config file. Account switching and reload
	// write to this file, so it must be a regular file.
	ConfigPath string
	// PublicBase is the base URL clients see, including the version segment
	// (for example https://host/api/llm-proxy/v1). Empty keeps the standalone
	// default of http://<listen>/v1.
	PublicBase string
	// Logger receives model listing and control-plane messages. Nil opens
	// LogFile, or the log file named by the config when LogFile is empty too.
	Logger *logutil.Logger
	// LogFile is where the embedded proxy appends its own messages. A host that
	// runs the standalone form under a supervisor passes the same path both
	// ways so switching modes never moves the log.
	LogFile string
}

// EmbeddedServer is a proxy running inside another Go program. It implements
// http.Handler so the host can mount it under any prefix.
type EmbeddedServer struct {
	handler *configHandler

	cancel  context.CancelFunc
	closer  interface{ Close() error }
	wg      sync.WaitGroup
	closeMu sync.Once
	closed  error
}

// StartEmbedded loads opts.ConfigPath and prepares the proxy. It does not bind
// a socket: mount Handler() on the host's mux.
func StartEmbedded(opts EmbedOptions) (*EmbeddedServer, error) {
	configPath := strings.TrimSpace(opts.ConfigPath)
	if configPath == "" {
		return nil, errors.New("embed llm-proxy: ConfigPath is required")
	}
	config, routes, err := loadProxyConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("embed llm-proxy: %w", err)
	}
	logger := opts.Logger
	logPath := strings.TrimSpace(opts.LogFile)
	var closer interface{ Close() error }
	if logger == nil {
		if logPath == "" {
			logPath, _ = logutil.ResolveLogFile(config.Log)
		}
		opened, openedCloser, err := logutil.OpenAppend(logPath)
		if err != nil {
			return nil, err
		}
		logger = opened
		if openedCloser != nil {
			closer = openedCloser
		}
	}
	handler, err := newConfigHandler(config, routes, logger)
	if err != nil {
		if closer != nil {
			closer.Close()
		}
		return nil, err
	}
	handler.configPath = configPath
	handler.logPath = logPath
	if base := strings.TrimRight(strings.TrimSpace(opts.PublicBase), "/"); base != "" {
		handler.endpoint = base
	}

	ctx, cancel := context.WithCancel(context.Background())
	server := &EmbeddedServer{handler: handler, cancel: cancel, closer: closer}
	server.wg.Add(1)
	go func() {
		defer server.wg.Done()
		handler.refreshModelListingLoop(ctx)
	}()
	return server, nil
}

// Handler serves /v1 models, /v1/{messages,responses,chat/completions}, and the
// /_llmproxy control endpoints.
func (s *EmbeddedServer) Handler() http.Handler { return s.handler }

// Endpoint reports the base URL advertised to clients.
func (s *EmbeddedServer) Endpoint() string { return s.handler.endpoint }

// ConfigPath reports the config file the proxy watches.
func (s *EmbeddedServer) ConfigPath() string { return s.handler.configPath }

// Status reports the live configuration and listing state.
func (s *EmbeddedServer) Status() StatusReport { return s.handler.Status() }

// Reload re-reads the config file. Failures are *AdminError values carrying the
// HTTP status a supervisor should surface.
func (s *EmbeddedServer) Reload() (ReloadReport, error) { return s.handler.Reload() }

// SelectCredential switches one provider's account and rewrites the config.
func (s *EmbeddedServer) SelectCredential(provider, credential string) (SelectReport, error) {
	return s.handler.SelectCredential(provider, credential)
}

// RefreshModelListing asks the listing loop to probe providers now.
func (s *EmbeddedServer) RefreshModelListing() { s.handler.wakeModelListingLoop() }

// Close stops background work. In-flight requests finish normally.
func (s *EmbeddedServer) Close() error {
	s.closeMu.Do(func() {
		s.cancel()
		s.wg.Wait()
		if s.closer != nil {
			s.closed = s.closer.Close()
		}
	})
	return s.closed
}
