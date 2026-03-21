package jsonrpc

import (
	"encoding/json"
	"fmt"
)

// Version is the JSON-RPC 2.0 version string.
const Version = "2.0"

// Error codes as defined by JSON-RPC 2.0 and MCP extensions.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	CodeAuthFailed     = -32000
)

// ID represents a JSON-RPC request ID, which can be a string, number, or null.
type ID struct {
	raw json.RawMessage
}

// StringID creates an ID from a string value.
func StringID(s string) *ID {
	b, _ := json.Marshal(s)
	return &ID{raw: b}
}

// NumberID creates an ID from an integer value.
func NumberID(n int64) *ID {
	b, _ := json.Marshal(n)
	return &ID{raw: b}
}

// NullID creates a null ID.
func NullID() *ID {
	return &ID{raw: json.RawMessage("null")}
}

// IsNull returns true if the ID is null.
func (id *ID) IsNull() bool {
	if id == nil {
		return true
	}
	return string(id.raw) == "null"
}

// MarshalJSON implements json.Marshaler.
func (id *ID) MarshalJSON() ([]byte, error) {
	if id == nil || len(id.raw) == 0 {
		return []byte("null"), nil
	}
	return id.raw, nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (id *ID) UnmarshalJSON(data []byte) error {
	id.raw = make(json.RawMessage, len(data))
	copy(id.raw, data)
	return nil
}

// String returns a human-readable representation of the ID.
func (id *ID) String() string {
	if id == nil {
		return "null"
	}
	return string(id.raw)
}

// Error represents a JSON-RPC 2.0 error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// Request represents a JSON-RPC 2.0 request or notification.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *ID             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification returns true if the request has no ID (i.e., it is a notification).
func (r *Request) IsNotification() bool {
	return r.ID == nil || r.ID.IsNull()
}

// Response represents a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *ID             `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// ErrorResponse constructs a Response representing an error.
func ErrorResponse(id *ID, code int, message string) Response {
	return Response{
		JSONRPC: Version,
		ID:      id,
		Error: &Error{
			Code:    code,
			Message: message,
		},
	}
}

// Marshal serializes v to JSON.
func Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}
