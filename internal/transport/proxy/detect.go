package proxy

import (
	"encoding/json"
	"net/http"
)

// Protocol identifies the communication protocol of an incoming request.
type Protocol int

const (
	ProtoAuto    Protocol = iota // Auto-detect from request content
	ProtoMCP                     // MCP JSON-RPC 2.0
	ProtoA2A                     // Google A2A JSON-RPC 2.0
	ProtoHTTPAPI                 // Plain REST / GraphQL
)

// String returns a human-readable protocol name.
func (p Protocol) String() string {
	switch p {
	case ProtoAuto:
		return "auto"
	case ProtoMCP:
		return "mcp"
	case ProtoA2A:
		return "a2a"
	case ProtoHTTPAPI:
		return "http"
	default:
		return "unknown"
	}
}

// a2aMethods lists JSON-RPC method names defined by the Google A2A protocol spec.
var a2aMethods = map[string]bool{
	"tasks/send":                 true,
	"tasks/get":                  true,
	"tasks/cancel":               true,
	"tasks/sendSubscribe":        true,
	"tasks/resubscribe":          true,
	"tasks/pushNotification/set": true,
	"tasks/pushNotification/get": true,
}

// jsonrpcProbe is a minimal structure for identifying JSON-RPC 2.0 messages.
type jsonrpcProbe struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
}

// DetectProtocol inspects HTTP headers and request body to determine the protocol.
// Detection follows a 3-stage pipeline: headers → JSON-RPC structure → method name.
func DetectProtocol(r *http.Request, body []byte) Protocol {
	// Stage 1: Header-based hints (fastest path).
	if r.Header.Get("X-A2A-Signature") != "" {
		return ProtoA2A
	}
	if r.Header.Get("Mcp-Session-Id") != "" {
		return ProtoMCP
	}

	// Stage 2: Check for JSON-RPC 2.0 envelope.
	var probe jsonrpcProbe
	if err := json.Unmarshal(body, &probe); err != nil {
		return ProtoHTTPAPI
	}
	if probe.JSONRPC != "2.0" || probe.Method == "" {
		return ProtoHTTPAPI
	}

	// Stage 3: Distinguish MCP from A2A by method name.
	if a2aMethods[probe.Method] {
		return ProtoA2A
	}

	return ProtoMCP
}

// ParseProtocolHint converts a configuration string to a Protocol constant.
func ParseProtocolHint(s string) Protocol {
	switch s {
	case "a2a":
		return ProtoA2A
	case "http", "rest", "graphql":
		return ProtoHTTPAPI
	case "mcp":
		return ProtoMCP
	default:
		return ProtoAuto
	}
}
