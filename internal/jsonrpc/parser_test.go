package jsonrpc

import (
	"io"
	"strings"
	"testing"
)

func TestParseRawRequest(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	msg := parseRaw(raw)
	if !msg.IsJSON {
		t.Fatal("expected IsJSON=true")
	}
	if msg.Request == nil {
		t.Fatal("expected Request != nil")
	}
	if msg.Request.Method != "tools/list" {
		t.Errorf("expected method=tools/list, got %q", msg.Request.Method)
	}
	if msg.Resp != nil {
		t.Error("expected Resp=nil for a request")
	}
}

func TestParseRawResponse(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)
	msg := parseRaw(raw)
	if !msg.IsJSON {
		t.Fatal("expected IsJSON=true")
	}
	if msg.Resp == nil {
		t.Fatal("expected Resp != nil")
	}
	if msg.Request != nil {
		t.Error("expected Request=nil for a response")
	}
}

func TestParseRawNotification(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","method":"notifications/message","params":{}}`)
	msg := parseRaw(raw)
	if !msg.IsJSON {
		t.Fatal("expected IsJSON=true")
	}
	if msg.Request == nil {
		t.Fatal("expected Request != nil for notification")
	}
	if !msg.Request.IsNotification() {
		t.Error("expected IsNotification()=true")
	}
}

func TestParseRawInvalidJSON(t *testing.T) {
	raw := []byte(`not json at all`)
	msg := parseRaw(raw)
	if msg.IsJSON {
		t.Fatal("expected IsJSON=false for invalid JSON")
	}
	if msg.Request != nil || msg.Resp != nil {
		t.Error("expected Request and Resp to be nil for invalid JSON")
	}
}

func TestParserNextNewline(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}
{"jsonrpc":"2.0","id":1,"result":{}}

{"jsonrpc":"2.0","method":"ping"}
`
	p := NewParser(strings.NewReader(input), ModeNewline)

	msg1, err := p.Next()
	if err != nil {
		t.Fatalf("Next() error: %v", err)
	}
	if msg1.Request == nil || msg1.Request.Method != "initialize" {
		t.Errorf("expected initialize request, got %+v", msg1)
	}

	msg2, err := p.Next()
	if err != nil {
		t.Fatalf("Next() error: %v", err)
	}
	if msg2.Resp == nil {
		t.Error("expected response for second message")
	}

	// Blank line is skipped, so next is the notification.
	msg3, err := p.Next()
	if err != nil {
		t.Fatalf("Next() error: %v", err)
	}
	if msg3.Request == nil || msg3.Request.Method != "ping" {
		t.Errorf("expected ping notification, got %+v", msg3)
	}

	_, err = p.Next()
	if err != io.EOF {
		t.Errorf("expected io.EOF, got %v", err)
	}
}

func TestIDTypes(t *testing.T) {
	s := StringID("abc")
	if s.IsNull() {
		t.Error("StringID should not be null")
	}

	n := NumberID(42)
	if n.IsNull() {
		t.Error("NumberID should not be null")
	}

	null := NullID()
	if !null.IsNull() {
		t.Error("NullID should be null")
	}
}
