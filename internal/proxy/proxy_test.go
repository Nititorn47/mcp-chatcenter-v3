package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func testConfig(t *testing.T, endpoint, bearer string) Config {
	t.Helper()
	parsed, err := url.Parse(endpoint + "?base=1")
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		UpstreamURL:       parsed,
		Bearer:            bearer,
		AllowClientBearer: true,
		Transport:         "http",
		HTTPAddr:          ":0",
		HTTPPath:          "/mcp",
		Timeout:           5 * time.Second,
	}
}

func newTestProxy(t *testing.T, upstream *httptest.Server, bearer string) *Proxy {
	t.Helper()
	handler, err := New(testConfig(t, upstream.URL, bearer))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestHTTPInjectsBearerAndProtocolHeaders(t *testing.T) {
	type captured struct {
		method  string
		query   string
		headers http.Header
		body    []byte
	}
	received := make(chan captured, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- captured{method: r.Method, query: r.URL.RawQuery, headers: r.Header.Clone(), body: body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer upstream.Close()

	handler := newTestProxy(t, upstream, "env-token")
	request := httptest.NewRequest(http.MethodPost, "/mcp?mode=readonly", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`))
	request.Header.Set("Origin", "https://evil.example")
	request.Header.Set("Accept", "text/html")
	request.Header.Set("Authorization", "")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	got := <-received
	if got.method != http.MethodPost {
		t.Fatalf("method = %s", got.method)
	}
	if got.query != "base=1&mode=readonly" {
		t.Fatalf("query = %s", got.query)
	}
	if value := got.headers.Get("Authorization"); value != "Bearer env-token" {
		t.Fatalf("Authorization = %q", value)
	}
	if value := got.headers.Get("Accept"); value != "application/json, text/event-stream" {
		t.Fatalf("Accept = %q", value)
	}
	if value := got.headers.Get("Content-Type"); value != "application/json" {
		t.Fatalf("Content-Type = %q", value)
	}
	if value := got.headers.Get("Origin"); value != "" {
		t.Fatalf("Origin leaked: %q", value)
	}
	if value := got.headers.Get("MCP-Protocol-Version"); value != ProtocolVersion2026 {
		t.Fatalf("MCP-Protocol-Version = %q", value)
	}
	if value := got.headers.Get("Mcp-Method"); value != "tools/list" {
		t.Fatalf("Mcp-Method = %q", value)
	}
}

func TestHTTPClientBearerOverridesEnvironment(t *testing.T) {
	authorization := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer upstream.Close()

	handler := newTestProxy(t, upstream, "env-token")
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	request.Header.Set("Authorization", "Bearer client-token")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if value := <-authorization; value != "Bearer client-token" {
		t.Fatalf("Authorization = %q", value)
	}
}

func TestHTTPIgnoresNonBearerClientAuthorization(t *testing.T) {
	authorization := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer upstream.Close()

	handler := newTestProxy(t, upstream, "env-token")
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	request.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if value := <-authorization; value != "Bearer env-token" {
		t.Fatalf("Authorization = %q", value)
	}
}

func TestHTTPPassesThroughSSEAndSessionHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Mcp-Session-Id", "session-1")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n"))
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n"))
	}))
	defer upstream.Close()

	handler := newTestProxy(t, upstream, "env-token")
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if value := recorder.Header().Get("Content-Type"); value != "text/event-stream" {
		t.Fatalf("Content-Type = %q", value)
	}
	if value := recorder.Header().Get("Mcp-Session-Id"); value != "session-1" {
		t.Fatalf("Mcp-Session-Id = %q", value)
	}
	if !strings.Contains(recorder.Body.String(), "notifications/progress") || strings.Count(recorder.Body.String(), "data:") != 2 {
		t.Fatalf("body = %q", recorder.Body.String())
	}
}

func TestHTTPHealth(t *testing.T) {
	handler := newTestProxy(t, httptest.NewServer(http.NotFoundHandler()), "token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestHTTPRejectsUnknownPathAndMethod(t *testing.T) {
	handler := newTestProxy(t, httptest.NewServer(http.NotFoundHandler()), "token")
	for _, test := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodPost, "/other", http.StatusNotFound},
		{http.MethodPut, "/mcp", http.StatusMethodNotAllowed},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))
		if recorder.Code != test.status {
			t.Fatalf("%s %s status = %d", test.method, test.path, recorder.Code)
		}
	}
}

func TestStdioBridgeForwardsMessagesAndTracksProtocolVersion(t *testing.T) {
	type call struct {
		authorization string
		version       string
		method        string
		name          string
	}
	var calls []call
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		envelope := parseEnvelope(body)
		calls = append(calls, call{
			authorization: r.Header.Get("Authorization"),
			version:       r.Header.Get("MCP-Protocol-Version"),
			method:        r.Header.Get("Mcp-Method"),
			name:          r.Header.Get("Mcp-Name"),
		})
		switch envelope.Method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"chatcenter"}}}`))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":4,\"result\":{\"content\":[]}}\n\n"))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":3,"result":{"tools":[]}}`))
		}
	}))
	defer upstream.Close()

	handler := newTestProxy(t, upstream, "env-token")
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"getCustomer","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := handler.RunStdio(t.Context(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("output lines = %d: %q", len(lines), output.String())
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("invalid JSON output: %q", line)
		}
	}
	if len(calls) != 4 {
		t.Fatalf("upstream calls = %d", len(calls))
	}
	for _, got := range calls {
		if got.authorization != "Bearer env-token" {
			t.Fatalf("Authorization = %q", got.authorization)
		}
	}
	if calls[0].version != "" {
		t.Fatalf("initialize version = %q", calls[0].version)
	}
	if calls[1].version != "2025-06-18" {
		t.Fatalf("initialized version = %q", calls[1].version)
	}
	if calls[2].version != "2025-06-18" {
		t.Fatalf("tools/list version = %q", calls[2].version)
	}
	if calls[3].version != ProtocolVersion2026 || calls[3].method != "tools/call" || calls[3].name != "getCustomer" {
		t.Fatalf("tools/call headers = %+v", calls[3])
	}
}

func TestStdioBridgeSynthesizesErrorOnUpstreamFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer upstream.Close()

	handler := newTestProxy(t, upstream, "env-token")
	var output bytes.Buffer
	if err := handler.RunStdio(t.Context(), strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}`+"\n"), &output); err != nil {
		t.Fatal(err)
	}
	var response struct {
		ID    int `json:"id"`
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("output = %q: %v", output.String(), err)
	}
	if response.ID != 7 || response.Error.Code != -32603 {
		t.Fatalf("response = %+v", response)
	}
}

func TestStdioBridgeRejectsNonJSONRPCUpstreamBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"unauthenticated","message":"bad token"}`))
	}))
	defer upstream.Close()

	handler := newTestProxy(t, upstream, "env-token")
	var output bytes.Buffer
	if err := handler.RunStdio(t.Context(), strings.NewReader(`{"jsonrpc":"2.0","id":8,"method":"tools/list"}`+"\n"), &output); err != nil {
		t.Fatal(err)
	}
	var response struct {
		ID    int `json:"id"`
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("output = %q: %v", output.String(), err)
	}
	if response.ID != 8 || response.Error.Code != -32603 {
		t.Fatalf("response = %+v", response)
	}
}

func TestStdioBridgeKeepsSessionHeader(t *testing.T) {
	var sessions []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessions = append(sessions, r.Header.Get("Mcp-Session-Id"))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "session-9")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}`))
	}))
	defer upstream.Close()

	handler := newTestProxy(t, upstream, "env-token")
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	var output bytes.Buffer
	if err := handler.RunStdio(t.Context(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions[0] != "" || sessions[1] != "session-9" {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestConfigRequiresUpstreamURL(t *testing.T) {
	t.Setenv("CHATCENTER_MCP_URL", "")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("expected an error")
	}
}

func TestConfigReadsEnvironment(t *testing.T) {
	t.Setenv("CHATCENTER_MCP_URL", "https://chat.example/chatcenter/api/v3/mcp")
	t.Setenv("CHATCENTER_API_BEARER", "Bearer secret")
	t.Setenv("MCP_TRANSPORT", "both")
	t.Setenv("MCP_HTTP_PATH", "/proxy")
	t.Setenv("CHATCENTER_HTTP_TIMEOUT", "15s")
	t.Setenv("CHATCENTER_TLS_INSECURE", "true")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Bearer != "secret" || cfg.Transport != "both" || cfg.HTTPPath != "/proxy" || cfg.Timeout != 15*time.Second || !cfg.InsecureTLS {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestConfigUsesPlatformPort(t *testing.T) {
	t.Setenv("CHATCENTER_MCP_URL", "https://chat.example/chatcenter/api/v3/mcp")
	t.Setenv("PORT", "8000")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8000" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
}

func TestConcurrentHTTPRequests(t *testing.T) {
	var mutex sync.Mutex
	bearers := map[string]bool{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		bearers[r.Header.Get("Authorization")] = true
		mutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer upstream.Close()

	handler := newTestProxy(t, upstream, "env-token")
	var wait sync.WaitGroup
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			token := "env-token"
			if index%2 == 0 {
				token = "client-token"
			}
			request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			request.Header.Set("Authorization", "Bearer "+token)
			handler.ServeHTTP(httptest.NewRecorder(), request)
		}(index)
	}
	wait.Wait()
	mutex.Lock()
	defer mutex.Unlock()
	if !bearers["Bearer env-token"] || !bearers["Bearer client-token"] {
		t.Fatalf("bearers = %#v", bearers)
	}
}
