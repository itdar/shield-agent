package telemetry

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

const ringSize = 10000

// Event is a single telemetry event.
type Event struct {
	AgentIDHash      string    `json:"agentIdHash"`
	Timestamp        time.Time `json:"timestamp"`
	Method           string    `json:"method"`
	Direction        string    `json:"direction"`   // "request" | "response"
	Success          bool      `json:"success"`
	LatencyMs        float64   `json:"latencyMs"`
	PayloadSizeBytes int       `json:"payloadSizeBytes"`
	AuthStatus       string    `json:"authStatus"`            // "verified" | "failed" | "unsigned"
	ErrorCode        string    `json:"errorCode,omitempty"`
}

// Collector buffers and periodically ships telemetry events.
type Collector struct {
	enabled       bool
	endpoint      string
	batchInterval time.Duration
	epsilon       float64
	salt          string
	mu            sync.Mutex
	ring          []Event
	head          int
	count         int
	logger        *slog.Logger
}

// New creates a new Collector. batchIntervalSec is in seconds.
func New(enabled bool, endpoint string, batchIntervalSec int, epsilon float64, salt string, logger *slog.Logger) *Collector {
	interval := time.Duration(batchIntervalSec) * time.Second
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return &Collector{
		enabled:       enabled,
		endpoint:      endpoint,
		batchInterval: interval,
		epsilon:       epsilon,
		salt:          salt,
		ring:          make([]Event, ringSize),
		logger:        logger,
	}
}

// Record adds an event to the ring buffer. No-op if not enabled.
func (c *Collector) Record(event Event) {
	if !c.enabled {
		return
	}

	event.AgentIDHash = c.hashID(event.AgentIDHash)

	c.mu.Lock()
	c.ring[c.head] = event
	c.head = (c.head + 1) % ringSize
	if c.count < ringSize {
		c.count++
	}
	shouldFlush := c.count >= ringSize
	c.mu.Unlock()

	if shouldFlush {
		c.flush()
	}
}

// Run starts the periodic flush loop. It blocks until ctx is cancelled.
func (c *Collector) Run(ctx context.Context) {
	if !c.enabled {
		return
	}

	ticker := time.NewTicker(c.batchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.flush()
		case <-ctx.Done():
			c.flush()
			return
		}
	}
}

// flush drains the ring buffer, applies DP noise, and sends.
func (c *Collector) flush() {
	c.mu.Lock()
	if c.count == 0 {
		c.mu.Unlock()
		return
	}

	events := make([]Event, c.count)
	// The ring is circular; oldest entry is at (head - count + ringSize) % ringSize.
	start := (c.head - c.count + ringSize) % ringSize
	for i := 0; i < c.count; i++ {
		events[i] = c.ring[(start+i)%ringSize]
	}
	c.head = 0
	c.count = 0
	c.mu.Unlock()

	c.applyDP(events)
	c.send(events)
}

// applyDP applies randomized response with probability 1/(1+e^epsilon) to flip Success.
func (c *Collector) applyDP(events []Event) {
	flipProb := 1.0 / (1.0 + math.Exp(c.epsilon))
	for i := range events {
		if rand.Float64() < flipProb {
			events[i].Success = !events[i].Success
		}
	}
}

// send gzip-compresses and POSTs events to the telemetry endpoint.
func (c *Collector) send(events []Event) {
	if len(events) == 0 {
		return
	}

	payload, err := json.Marshal(events)
	if err != nil {
		c.logger.Debug("telemetry: json marshal error", slog.String("error", err.Error()))
		return
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(payload); err != nil {
		c.logger.Debug("telemetry: gzip write error", slog.String("error", err.Error()))
		return
	}
	if err := gz.Close(); err != nil {
		c.logger.Debug("telemetry: gzip close error", slog.String("error", err.Error()))
		return
	}

	url := c.endpoint + "/telemetry/ingest"
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		c.logger.Debug("telemetry: creating request failed", slog.String("error", err.Error()))
		return
	}
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.logger.Debug("telemetry: send error", slog.String("error", err.Error()))
		return
	}
	defer resp.Body.Close()

	c.logger.Debug("telemetry: sent batch",
		slog.Int("events", len(events)),
		slog.Int("status", resp.StatusCode),
	)
}

// hashID computes sha256(salt + id) as a hex string.
func (c *Collector) hashID(id string) string {
	h := sha256.Sum256([]byte(c.salt + id))
	return hex.EncodeToString(h[:])
}

// MaskIP applies k-anonymity by truncating the last octet(s) of an IP address.
// IPv4: keeps /24 prefix, e.g. "203.0.113.45" → "203.0.113.0"
// IPv6: keeps /48 prefix, e.g. "2001:db8:1234::1" → "2001:db8:1234::"
// Returns the original string unchanged if it cannot be parsed as an IP.
func MaskIP(ip string) string {
	// Try IPv4 first (plain a.b.c.d form).
	parts := strings.Split(ip, ".")
	if len(parts) == 4 {
		allNumeric := true
		for _, p := range parts {
			for _, ch := range p {
				if ch < '0' || ch > '9' {
					allNumeric = false
					break
				}
			}
		}
		if allNumeric {
			return parts[0] + "." + parts[1] + "." + parts[2] + ".0"
		}
	}

	// Try IPv6 (colon-separated).
	if strings.Contains(ip, ":") {
		// Expand shorthand and keep first 3 groups.
		groups := strings.Split(ip, ":")
		if len(groups) >= 3 {
			return groups[0] + ":" + groups[1] + ":" + groups[2] + "::"
		}
	}

	return ip
}
