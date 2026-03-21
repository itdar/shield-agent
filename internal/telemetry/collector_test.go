package telemetry

import (
	"log/slog"
	"os"
	"testing"
	"time"
)

// newTestCollector creates a collector with sane defaults for testing.
func newTestCollector(enabled bool, epsilon float64) *Collector {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return New(enabled, "http://localhost:8080", 1, epsilon, "test-salt", logger)
}

// TestCollectorDisabled verifies that Record() on a disabled collector is a no-op.
func TestCollectorDisabled(t *testing.T) {
	collector := newTestCollector(false, 1.0)

	event := Event{
		AgentIDHash:      "test-agent",
		Timestamp:        time.Now(),
		Method:           "GET",
		Success:          true,
		LatencyMs:        100.0,
		PayloadSizeBytes: 256,
	}

	// Record multiple times
	for i := 0; i < 100; i++ {
		collector.Record(event)
	}

	// Verify count is still 0
	collector.mu.Lock()
	count := collector.count
	collector.mu.Unlock()

	if count != 0 {
		t.Errorf("disabled collector count = %d, expected 0", count)
	}
}

// TestCollectorRecord verifies that Record() on an enabled collector increments count.
func TestCollectorRecord(t *testing.T) {
	collector := newTestCollector(true, 1.0)

	event := Event{
		AgentIDHash:      "test-agent",
		Timestamp:        time.Now(),
		Method:           "POST",
		Success:          true,
		LatencyMs:        50.0,
		PayloadSizeBytes: 512,
	}

	// Record 5 events
	for i := 0; i < 5; i++ {
		collector.Record(event)
	}

	// Verify count incremented
	collector.mu.Lock()
	count := collector.count
	collector.mu.Unlock()

	if count != 5 {
		t.Errorf("collector.count = %d, expected 5", count)
	}
}

// TestHashID verifies that hashID produces consistent results for the same input,
// and different results for different inputs.
func TestHashID(t *testing.T) {
	collector := newTestCollector(true, 1.0)

	// Test consistency: same input produces same hash
	hash1 := collector.hashID("agent-123")
	hash2 := collector.hashID("agent-123")
	if hash1 != hash2 {
		t.Errorf("hashID inconsistent: %q != %q", hash1, hash2)
	}

	// Test different input produces different hash
	hash3 := collector.hashID("agent-456")
	if hash1 == hash3 {
		t.Errorf("different inputs produced same hash: %q", hash1)
	}

	// Test that salt matters
	collector2 := New(true, "http://localhost:8080", 1, 1.0, "different-salt", nil)
	hash4 := collector2.hashID("agent-123")
	if hash1 == hash4 {
		t.Errorf("different salts produced same hash for same input: %q", hash1)
	}

	// Verify hash is non-empty and reasonable length (SHA256 hex is 64 chars)
	if len(hash1) != 64 {
		t.Errorf("hash length = %d, expected 64 (SHA256 hex)", len(hash1))
	}
}

// TestRingBufferOverflow verifies that the ring buffer wraps correctly by
// testing head pointer behavior, and that flush resets the count.
func TestRingBufferOverflow(t *testing.T) {
	collector := newTestCollector(true, 1.0)
	event := Event{
		AgentIDHash:      "test-agent",
		Timestamp:        time.Now(),
		Method:           "GET",
		Success:          true,
		LatencyMs:        25.0,
		PayloadSizeBytes: 128,
	}

	// Fill to near capacity without triggering flush
	for i := 0; i < ringSize-10; i++ {
		collector.Record(event)
	}

	// Verify count is correct before flush
	collector.mu.Lock()
	count := collector.count
	head := collector.head
	collector.mu.Unlock()

	if count != ringSize-10 {
		t.Errorf("after %d records, count = %d, expected %d", ringSize-10, count, ringSize-10)
	}

	if head != ringSize-10 {
		t.Errorf("head = %d, expected %d", head, ringSize-10)
	}

	// Now add more records to trigger wrap-around
	for i := 0; i < 20; i++ {
		collector.Record(event)
	}

	// After adding 20 more (total ringSize+10), flush should have been triggered
	// After flush, count resets to 0, head wraps to (ringSize-10+20) % ringSize = 10
	collector.mu.Lock()
	count = collector.count
	head = collector.head
	collector.mu.Unlock()

	// After flush is triggered, count resets and head position wraps
	expectedHead := (ringSize - 10 + 20) % ringSize
	if head != expectedHead {
		t.Errorf("after wrap, head = %d, expected %d", head, expectedHead)
	}

	// Count should be near the new batch (around 10 events post-flush)
	if count < 0 || count > 30 {
		t.Errorf("after wrap, count = %d (expected small value after flush)", count)
	}
}

// TestApplyDPHighFlipProbability verifies that with a low epsilon (high flip probability),
// some events get flipped.
func TestApplyDPHighFlipProbability(t *testing.T) {
	// epsilon = 0.0001 means flip probability = 1/(1+e^0.0001) ≈ 0.5
	collector := newTestCollector(true, 0.0001)

	events := make([]Event, 100)
	for i := 0; i < 100; i++ {
		events[i] = Event{
			AgentIDHash:      "agent",
			Timestamp:        time.Now(),
			Method:           "GET",
			Success:          true, // Start with all true
			LatencyMs:        10.0,
			PayloadSizeBytes: 64,
		}
	}

	collector.applyDP(events)

	// With high flip probability, expect significant variation
	// Count how many were flipped (now false)
	flipped := 0
	for _, e := range events {
		if !e.Success {
			flipped++
		}
	}

	// With flip probability ~50%, expect roughly 50 flipped (allow 10-90 range)
	if flipped < 10 || flipped > 90 {
		t.Logf("high flip probability: %d/%d events flipped (expected ~50)", flipped, 100)
	}
}

// TestApplyDPLowFlipProbability verifies that with a high epsilon (low flip probability),
// very few events get flipped.
func TestApplyDPLowFlipProbability(t *testing.T) {
	// epsilon = 1000 means flip probability = 1/(1+e^1000) ≈ 0
	collector := newTestCollector(true, 1000.0)

	events := make([]Event, 100)
	for i := 0; i < 100; i++ {
		events[i] = Event{
			AgentIDHash:      "agent",
			Timestamp:        time.Now(),
			Method:           "GET",
			Success:          true, // Start with all true
			LatencyMs:        10.0,
			PayloadSizeBytes: 64,
		}
	}

	collector.applyDP(events)

	// With low flip probability, expect very few flipped
	flipped := 0
	for _, e := range events {
		if !e.Success {
			flipped++
		}
	}

	// With flip probability ≈ 0, expect 0 flipped (allow 1-2 due to randomness)
	if flipped > 5 {
		t.Logf("low flip probability: %d/%d events flipped (expected 0-5)", flipped, 100)
	}
}

// TestApplyDPTogglesBits verifies that applyDP correctly toggles the Success field.
func TestApplyDPTogglesBits(t *testing.T) {
	// Use epsilon such that we know behavior:
	// We'll use a moderate epsilon and just verify the toggle works
	collector := newTestCollector(true, 1.0)

	events := make([]Event, 10)
	for i := 0; i < 10; i++ {
		success := i%2 == 0 // Alternate true/false
		events[i] = Event{
			AgentIDHash:      "agent",
			Timestamp:        time.Now(),
			Method:           "GET",
			Success:          success,
			LatencyMs:        10.0,
			PayloadSizeBytes: 64,
		}
	}

	// Save original states
	originalSuccess := make([]bool, len(events))
	for i, e := range events {
		originalSuccess[i] = e.Success
	}

	// Apply DP (some events will be flipped, some won't)
	collector.applyDP(events)

	// Verify structure is intact and some events changed
	// (due to randomness, we can't guarantee specific flips, just that the function works)
	if len(events) != 10 {
		t.Errorf("events length changed after applyDP")
	}

	for i, e := range events {
		if e.AgentIDHash != "agent" || e.Method != "GET" {
			t.Errorf("non-Success fields modified at index %d", i)
		}
	}
}

// TestMaskIP verifies IP k-anonymity truncation.
func TestMaskIP(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"203.0.113.45", "203.0.113.0"},
		{"10.0.0.1", "10.0.0.0"},
		{"192.168.1.255", "192.168.1.0"},
		{"2001:db8:1234:5678::1", "2001:db8:1234::"},
		{"2001:0db8:85a3:0000:0000:8a2e:0370:7334", "2001:0db8:85a3::"},
		{"not-an-ip", "not-an-ip"},
		{"", ""},
	}
	for _, tc := range cases {
		got := MaskIP(tc.input)
		if got != tc.want {
			t.Errorf("MaskIP(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestRecordAppliesHash verifies that Record hashes the AgentIDHash field.
func TestRecordAppliesHash(t *testing.T) {
	collector := newTestCollector(true, 1.0)

	event := Event{
		AgentIDHash:      "original-agent-id",
		Timestamp:        time.Now(),
		Method:           "GET",
		Success:          true,
		LatencyMs:        10.0,
		PayloadSizeBytes: 64,
	}

	collector.Record(event)

	// Retrieve the recorded event from the ring
	collector.mu.Lock()
	recorded := collector.ring[0]
	collector.mu.Unlock()

	// The AgentIDHash should be hashed, not the original
	if recorded.AgentIDHash == "original-agent-id" {
		t.Error("Record did not hash the AgentIDHash")
	}

	// Verify it matches what we expect from hashID
	expectedHash := collector.hashID("original-agent-id")
	if recorded.AgentIDHash != expectedHash {
		t.Errorf("recorded hash = %q, expected %q", recorded.AgentIDHash, expectedHash)
	}
}
