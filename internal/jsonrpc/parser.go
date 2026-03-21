package jsonrpc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/textproto"
	"strconv"
	"strings"
)

// ParseMode controls how the parser splits the byte stream into messages.
type ParseMode int

const (
	// ModeAuto defaults to newline-delimited parsing.
	ModeAuto ParseMode = iota
	// ModeNewline expects one JSON message per line.
	ModeNewline
	// ModeContentLength expects HTTP-style Content-Length headers.
	ModeContentLength
)

// Message is a parsed JSON-RPC frame. Exactly one of Request or Resp will be
// set when IsJSON is true.
type Message struct {
	Raw     []byte
	Request *Request
	Resp    *Response
	IsJSON  bool
}

// Parser reads JSON-RPC messages from a stream.
type Parser struct {
	r    io.Reader
	mode ParseMode
	sc   *bufio.Scanner   // used in newline mode
	tr   *textproto.Reader // used in content-length mode
	br   *bufio.Reader
}

// NewParser creates a new Parser reading from r in the given mode.
func NewParser(r io.Reader, mode ParseMode) *Parser {
	p := &Parser{r: r, mode: mode}
	if mode == ModeAuto {
		p.mode = ModeNewline
	}
	if p.mode == ModeNewline {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		p.sc = sc
	} else {
		p.br = bufio.NewReader(r)
		p.tr = textproto.NewReader(p.br)
	}
	return p
}

// Next reads the next message from the stream.
// Returns io.EOF when the stream ends cleanly.
func (p *Parser) Next() (Message, error) {
	switch p.mode {
	case ModeNewline:
		return p.nextNewline()
	case ModeContentLength:
		return p.nextContentLength()
	default:
		return p.nextNewline()
	}
}

func (p *Parser) nextNewline() (Message, error) {
	for {
		if !p.sc.Scan() {
			if err := p.sc.Err(); err != nil {
				return Message{}, err
			}
			return Message{}, io.EOF
		}
		line := p.sc.Bytes()
		// Skip blank lines.
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			continue
		}
		raw := make([]byte, len(line))
		copy(raw, line)
		return parseRaw(raw), nil
	}
}

func (p *Parser) nextContentLength() (Message, error) {
	// Read MIME-style headers until blank line.
	mimeHeaders, err := p.tr.ReadMIMEHeader()
	if err != nil {
		if err == io.EOF {
			return Message{}, io.EOF
		}
		return Message{}, fmt.Errorf("reading headers: %w", err)
	}

	clStr := mimeHeaders.Get("Content-Length")
	if clStr == "" {
		return Message{}, fmt.Errorf("missing Content-Length header")
	}
	cl, err := strconv.Atoi(clStr)
	if err != nil || cl < 0 {
		return Message{}, fmt.Errorf("invalid Content-Length: %q", clStr)
	}

	raw := make([]byte, cl)
	if _, err := io.ReadFull(p.br, raw); err != nil {
		return Message{}, fmt.Errorf("reading body: %w", err)
	}
	return parseRaw(raw), nil
}

// parseRaw attempts to unmarshal raw as a JSON-RPC request or response.
func parseRaw(raw []byte) Message {
	msg := Message{Raw: raw}

	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		// Not valid JSON.
		msg.IsJSON = false
		return msg
	}
	msg.IsJSON = true

	if _, hasMethod := probe["method"]; hasMethod {
		var req Request
		if err := json.Unmarshal(raw, &req); err == nil {
			msg.Request = &req
		}
	} else {
		var resp Response
		if err := json.Unmarshal(raw, &resp); err == nil {
			msg.Resp = &resp
		}
	}
	return msg
}
