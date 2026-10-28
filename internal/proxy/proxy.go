package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"time"
)

const userAgent = "pam-chatcenter-mcp-proxy/1.0"

var forwardedRequestHeaders = []string{
	"MCP-Protocol-Version",
	"Mcp-Method",
	"Mcp-Name",
	"Mcp-Session-Id",
	"Last-Event-ID",
	"Idempotency-Key",
	"X-Request-Id",
	"Traceparent",
	"Tracestate",
}

var forwardedResponseHeaders = []string{
	"Content-Type",
	"Mcp-Session-Id",
	"MCP-Protocol-Version",
	"Cache-Control",
	"Retry-After",
	"WWW-Authenticate",
	"X-Request-Id",
}

type Proxy struct {
	cfg      Config
	client   *http.Client
	upstream *url.URL
}

func New(cfg Config) (*Proxy, error) {
	if cfg.UpstreamURL == nil {
		return nil, errors.New("upstream URL is required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.ResponseHeaderTimeout = cfg.Timeout
	transport.TLSHandshakeTimeout = 10 * time.Second
	if cfg.InsecureTLS {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	upstream := *cfg.UpstreamURL
	return &Proxy{
		cfg: cfg,
		client: &http.Client{
			Transport: transport,
			Timeout:   cfg.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		upstream: &upstream,
	}, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz", "/readinez":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
		return
	}
	if r.URL.Path != p.cfg.HTTPPath {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodGet && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	target := *p.upstream
	target.RawQuery = mergeQuery(target.Query(), r.URL.Query()).Encode()
	request, err := p.newUpstreamRequest(r.Context(), r.Method, &target, body, r.Header, "")
	if err != nil {
		http.Error(w, "cannot build upstream request", http.StatusInternalServerError)
		return
	}
	response, err := p.client.Do(request)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		http.Error(w, "upstream request failed", status)
		return
	}
	defer response.Body.Close()

	copyHeaders(w.Header(), response.Header, forwardedResponseHeaders)
	w.WriteHeader(response.StatusCode)
	streamResponse(w, response.Body)
}

func (p *Proxy) RunHTTP(ctx context.Context) error {
	server := &http.Server{
		Addr:              p.cfg.HTTPAddr,
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("mcp proxy listening on %s%s", p.cfg.HTTPAddr, p.cfg.HTTPPath)
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

func (p *Proxy) newUpstreamRequest(ctx context.Context, method string, target *url.URL, body []byte, inbound http.Header, fallbackVersion string) (*http.Request, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("User-Agent", userAgent)

	token := p.cfg.Bearer
	if inbound != nil && p.cfg.AllowClientBearer {
		if client := clientBearer(inbound.Get("Authorization")); client != "" {
			token = client
		}
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if inbound != nil {
		for _, name := range forwardedRequestHeaders {
			if value := inbound.Get(name); value != "" {
				request.Header.Set(name, value)
			}
		}
	}

	version := request.Header.Get("MCP-Protocol-Version")
	if version == "" {
		version = bodyProtocolVersion(body)
	}
	if version == "" {
		version = fallbackVersion
	}
	if version != "" {
		request.Header.Set("MCP-Protocol-Version", version)
	}
	if version == ProtocolVersion2026 {
		envelope := parseEnvelope(body)
		if request.Header.Get("Mcp-Method") == "" {
			request.Header.Set("Mcp-Method", envelope.Method)
		}
		if envelope.Method == "tools/call" && request.Header.Get("Mcp-Name") == "" {
			request.Header.Set("Mcp-Name", envelope.Name)
		}
	}
	return request, nil
}

func mergeQuery(base, incoming url.Values) url.Values {
	if len(incoming) == 0 {
		return base
	}
	merged := url.Values{}
	for key, values := range base {
		merged[key] = append([]string(nil), values...)
	}
	for key, values := range incoming {
		merged[key] = append([]string(nil), values...)
	}
	return merged
}

func copyHeaders(destination, source http.Header, names []string) {
	for _, name := range names {
		if values, present := source[http.CanonicalHeaderKey(name)]; present {
			destination[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
		}
	}
}

func streamResponse(w http.ResponseWriter, body io.Reader) {
	buffer := make([]byte, 32*1024)
	flusher, _ := w.(http.Flusher)
	for {
		read, err := body.Read(buffer)
		if read > 0 {
			if _, writeErr := w.Write(buffer[:read]); writeErr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

type rpcEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Name   string          `json:"-"`
	Meta   string          `json:"-"`
}

func parseEnvelope(body []byte) rpcEnvelope {
	var raw struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name string                     `json:"name"`
			Meta map[string]json.RawMessage `json:"_meta"`
		} `json:"params"`
	}
	if len(body) == 0 || json.Unmarshal(body, &raw) != nil {
		return rpcEnvelope{ID: json.RawMessage("null")}
	}
	envelope := rpcEnvelope{ID: raw.ID, Method: raw.Method, Name: raw.Params.Name}
	if version, ok := raw.Params.Meta["io.modelcontextprotocol/protocolVersion"]; ok {
		_ = json.Unmarshal(version, &envelope.Meta)
	}
	if len(envelope.ID) == 0 {
		envelope.ID = json.RawMessage("null")
	}
	return envelope
}

func bodyProtocolVersion(body []byte) string {
	return parseEnvelope(body).Meta
}

func isEventStream(contentType string) bool {
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return media == "text/event-stream"
}
