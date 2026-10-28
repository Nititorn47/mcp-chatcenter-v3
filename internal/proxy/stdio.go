package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
)

type stdioBridge struct {
	proxy   *Proxy
	version string
	session string
}

func (p *Proxy) RunStdio(ctx context.Context, input io.Reader, output io.Writer) error {
	bridge := &stdioBridge{proxy: p}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), maxBodyBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) {
			log.Printf("stdio: ignoring a line that is not JSON")
			continue
		}
		if err := bridge.handle(ctx, append([]byte(nil), line...), output); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
	}
	return scanner.Err()
}

func (b *stdioBridge) handle(ctx context.Context, body []byte, output io.Writer) error {
	envelope := parseEnvelope(body)
	header := http.Header{}
	if b.session != "" {
		header.Set("Mcp-Session-Id", b.session)
	}

	request, err := b.proxy.newUpstreamRequest(ctx, http.MethodPost, b.proxy.upstream, body, header, b.version)
	if err != nil {
		return writeRPCError(output, envelope.ID, -32603, "cannot build upstream request")
	}
	response, err := b.proxy.client.Do(request)
	if err != nil {
		log.Printf("stdio: upstream request failed: %v", err)
		return writeRPCError(output, envelope.ID, -32603, "upstream request failed")
	}
	defer response.Body.Close()

	if session := response.Header.Get("Mcp-Session-Id"); session != "" {
		b.session = session
	}

	messages, err := readStdioMessages(response)
	if err != nil {
		log.Printf("stdio: upstream response failed: %v", err)
		return writeRPCError(output, envelope.ID, -32603, "upstream response failed")
	}
	if len(messages) == 0 && response.StatusCode >= http.StatusBadRequest {
		return writeRPCError(output, envelope.ID, -32603, "upstream returned status "+response.Status)
	}
	for _, message := range messages {
		if !isJSONRPC(message) {
			log.Printf("stdio: upstream returned a message that is not JSON-RPC")
			if err := writeRPCError(output, envelope.ID, -32603, "upstream returned an invalid response"); err != nil {
				return err
			}
			continue
		}
		if version := responseProtocolVersion(message); version != "" {
			b.version = version
		}
		if _, err := output.Write(append(bytes.TrimSpace(message), '\n')); err != nil {
			return err
		}
	}
	return nil
}

func readStdioMessages(response *http.Response) ([][]byte, error) {
	if response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if isEventStream(response.Header.Get("Content-Type")) {
		return readEventStream(response.Body)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes))
	if err != nil {
		return nil, err
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, nil
	}
	return [][]byte{body}, nil
}

func readEventStream(body io.Reader) ([][]byte, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), maxBodyBytes)
	var data []string
	var messages [][]byte
	flush := func() {
		if len(data) == 0 {
			return
		}
		messages = append(messages, []byte(strings.Join(data, "\n")))
		data = nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			value = strings.TrimPrefix(value, " ")
			data = append(data, value)
		}
	}
	flush()
	return messages, scanner.Err()
}

func isJSONRPC(message []byte) bool {
	var probe struct {
		JSONRPC string `json:"jsonrpc"`
	}
	return json.Unmarshal(message, &probe) == nil && probe.JSONRPC == "2.0"
}

func responseProtocolVersion(message []byte) string {
	var response struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if json.Unmarshal(message, &response) != nil {
		return ""
	}
	return response.Result.ProtocolVersion
}

func writeRPCError(output io.Writer, id json.RawMessage, code int, message string) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	payload, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	})
	if err != nil {
		return err
	}
	_, err = output.Write(append(payload, '\n'))
	return err
}
