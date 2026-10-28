package proxy

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	ProtocolVersion2026 = "2026-07-28"
	maxBodyBytes        = 16 << 20
)

type Config struct {
	UpstreamURL       *url.URL
	Bearer            string
	AllowClientBearer bool
	Transport         string
	HTTPAddr          string
	HTTPPath          string
	Timeout           time.Duration
	InsecureTLS       bool
}

func ConfigFromEnv() (Config, error) {
	rawURL := strings.TrimSpace(os.Getenv("CHATCENTER_MCP_URL"))
	if rawURL == "" {
		return Config{}, errors.New("CHATCENTER_MCP_URL is required")
	}
	upstream, err := url.Parse(rawURL)
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		return Config{}, fmt.Errorf("CHATCENTER_MCP_URL must be an absolute URL")
	}
	if upstream.Scheme != "http" && upstream.Scheme != "https" {
		return Config{}, fmt.Errorf("CHATCENTER_MCP_URL must use http or https")
	}

	transport := strings.ToLower(strings.TrimSpace(os.Getenv("MCP_TRANSPORT")))
	if transport == "" {
		transport = "http"
	}
	if transport != "http" && transport != "stdio" && transport != "both" {
		return Config{}, fmt.Errorf("MCP_TRANSPORT must be http, stdio or both")
	}

	addr := strings.TrimSpace(os.Getenv("MCP_HTTP_ADDR"))
	if addr == "" {
		if port := strings.TrimSpace(os.Getenv("PORT")); port != "" {
			parsed, err := strconv.Atoi(port)
			if err != nil || parsed < 1 || parsed > 65535 {
				return Config{}, fmt.Errorf("PORT must be a valid TCP port")
			}
			addr = ":" + port
		} else {
			addr = ":8080"
		}
	}
	path := strings.TrimSpace(os.Getenv("MCP_HTTP_PATH"))
	if path == "" {
		path = "/mcp"
	}
	if !strings.HasPrefix(path, "/") {
		return Config{}, fmt.Errorf("MCP_HTTP_PATH must start with /")
	}

	timeout := 60 * time.Second
	if raw := strings.TrimSpace(os.Getenv("CHATCENTER_HTTP_TIMEOUT")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("CHATCENTER_HTTP_TIMEOUT must be a positive duration")
		}
		timeout = parsed
	}

	return Config{
		UpstreamURL:       upstream,
		Bearer:            normalizeBearer(os.Getenv("CHATCENTER_API_BEARER")),
		AllowClientBearer: envBool("CHATCENTER_ALLOW_CLIENT_BEARER", true),
		Transport:         transport,
		HTTPAddr:          addr,
		HTTPPath:          path,
		Timeout:           timeout,
		InsecureTLS:       envBool("CHATCENTER_TLS_INSECURE", false),
	}, nil
}

func normalizeBearer(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 7 && strings.EqualFold(value[:7], "Bearer ") {
		value = strings.TrimSpace(value[7:])
	}
	return value
}

func clientBearer(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 7 || !strings.EqualFold(value[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(value[7:])
}

func envBool(name string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return parsed
}
