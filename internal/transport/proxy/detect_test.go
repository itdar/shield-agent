package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDetectProtocol_HeaderHints(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		body     string
		expected Protocol
	}{
		{
			name:     "X-A2A-Signature header → A2A",
			headers:  map[string]string{"X-A2A-Signature": "abc123"},
			body:     `{"jsonrpc":"2.0","method":"tools/list"}`,
			expected: ProtoA2A,
		},
		{
			name:     "Mcp-Session-Id header → MCP",
			headers:  map[string]string{"Mcp-Session-Id": "sess-1"},
			body:     `{"jsonrpc":"2.0","method":"tasks/send"}`,
			expected: ProtoMCP,
		},
		{
			name:     "A2A header takes priority over MCP header",
			headers:  map[string]string{"X-A2A-Signature": "sig", "Mcp-Session-Id": "sess"},
			body:     `{}`,
			expected: ProtoA2A,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			for k, v := range tt.headers {
				r.Header.Set(k, v)
			}
			got := DetectProtocol(r, []byte(tt.body))
			if got != tt.expected {
				t.Errorf("DetectProtocol() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestDetectProtocol_JSONRPCStructure(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected Protocol
	}{
		{
			name:     "valid MCP JSON-RPC",
			body:     `{"jsonrpc":"2.0","method":"tools/list","id":1}`,
			expected: ProtoMCP,
		},
		{
			name:     "valid A2A JSON-RPC (tasks/send)",
			body:     `{"jsonrpc":"2.0","method":"tasks/send","id":1}`,
			expected: ProtoA2A,
		},
		{
			name:     "valid A2A JSON-RPC (tasks/get)",
			body:     `{"jsonrpc":"2.0","method":"tasks/get","id":2}`,
			expected: ProtoA2A,
		},
		{
			name:     "valid A2A JSON-RPC (tasks/cancel)",
			body:     `{"jsonrpc":"2.0","method":"tasks/cancel","id":3}`,
			expected: ProtoA2A,
		},
		{
			name:     "valid A2A JSON-RPC (tasks/sendSubscribe)",
			body:     `{"jsonrpc":"2.0","method":"tasks/sendSubscribe","id":4}`,
			expected: ProtoA2A,
		},
		{
			name:     "valid A2A JSON-RPC (tasks/pushNotification/set)",
			body:     `{"jsonrpc":"2.0","method":"tasks/pushNotification/set","id":5}`,
			expected: ProtoA2A,
		},
		{
			name:     "valid A2A JSON-RPC (tasks/pushNotification/get)",
			body:     `{"jsonrpc":"2.0","method":"tasks/pushNotification/get","id":6}`,
			expected: ProtoA2A,
		},
		{
			name:     "valid A2A JSON-RPC (tasks/resubscribe)",
			body:     `{"jsonrpc":"2.0","method":"tasks/resubscribe","id":7}`,
			expected: ProtoA2A,
		},
		{
			name:     "MCP method initialize",
			body:     `{"jsonrpc":"2.0","method":"initialize","id":1}`,
			expected: ProtoMCP,
		},
		{
			name:     "MCP method resources/read",
			body:     `{"jsonrpc":"2.0","method":"resources/read","id":1}`,
			expected: ProtoMCP,
		},
		{
			name:     "MCP method prompts/get",
			body:     `{"jsonrpc":"2.0","method":"prompts/get","id":1}`,
			expected: ProtoMCP,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			got := DetectProtocol(r, []byte(tt.body))
			if got != tt.expected {
				t.Errorf("DetectProtocol() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestDetectProtocol_NonJSONRPC(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected Protocol
	}{
		{
			name:     "invalid JSON → HTTP API",
			body:     `not json at all`,
			expected: ProtoHTTPAPI,
		},
		{
			name:     "empty body → HTTP API",
			body:     ``,
			expected: ProtoHTTPAPI,
		},
		{
			name:     "JSON without jsonrpc field → HTTP API",
			body:     `{"method":"tasks/send"}`,
			expected: ProtoHTTPAPI,
		},
		{
			name:     "JSON with wrong jsonrpc version → HTTP API",
			body:     `{"jsonrpc":"1.0","method":"tools/list"}`,
			expected: ProtoHTTPAPI,
		},
		{
			name:     "JSON-RPC without method → HTTP API",
			body:     `{"jsonrpc":"2.0","result":{}}`,
			expected: ProtoHTTPAPI,
		},
		{
			name:     "plain REST JSON body → HTTP API",
			body:     `{"name":"test","value":42}`,
			expected: ProtoHTTPAPI,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/data", nil)
			got := DetectProtocol(r, []byte(tt.body))
			if got != tt.expected {
				t.Errorf("DetectProtocol() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestParseProtocolHint(t *testing.T) {
	tests := []struct {
		input    string
		expected Protocol
	}{
		{"auto", ProtoAuto},
		{"mcp", ProtoMCP},
		{"a2a", ProtoA2A},
		{"http", ProtoHTTPAPI},
		{"rest", ProtoHTTPAPI},
		{"graphql", ProtoHTTPAPI},
		{"", ProtoAuto},
		{"unknown", ProtoAuto},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParseProtocolHint(tt.input)
			if got != tt.expected {
				t.Errorf("ParseProtocolHint(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestProtocol_String(t *testing.T) {
	tests := []struct {
		proto    Protocol
		expected string
	}{
		{ProtoAuto, "auto"},
		{ProtoMCP, "mcp"},
		{ProtoA2A, "a2a"},
		{ProtoHTTPAPI, "http"},
		{Protocol(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := tt.proto.String()
			if got != tt.expected {
				t.Errorf("Protocol(%d).String() = %q, want %q", tt.proto, got, tt.expected)
			}
		})
	}
}
