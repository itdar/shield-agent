package egress

import (
	"context"
	"errors"
	"testing"
	"time"
)

// recordingMW records which methods were called in order, so the test can
// assert that ProcessResponse runs in reverse-order of ProcessRequest.
type recordingMW struct {
	name  string
	log   *[]string
	reqErr error
}

func (r recordingMW) Name() string { return r.name }

func (r recordingMW) ProcessRequest(_ context.Context, req *Request) (*Request, error) {
	*r.log = append(*r.log, "req:"+r.name)
	if r.reqErr != nil {
		return nil, r.reqErr
	}
	return req, nil
}

func (r recordingMW) ProcessResponse(_ context.Context, _ *Request, resp *Response) (*Response, error) {
	*r.log = append(*r.log, "resp:"+r.name)
	return resp, nil
}

func TestEgressChainOrder(t *testing.T) {
	var log []string
	chain := NewEgressChain(
		recordingMW{name: "a", log: &log},
		recordingMW{name: "b", log: &log},
		recordingMW{name: "c", log: &log},
	)
	req := &Request{Destination: "api.openai.com:443", StartedAt: time.Now()}
	_, bad, err := chain.ProcessRequest(context.Background(), req)
	if err != nil || bad != nil {
		t.Fatalf("unexpected request error: %v", err)
	}
	if _, err := chain.ProcessResponse(context.Background(), req, &Response{}); err != nil {
		t.Fatalf("unexpected response error: %v", err)
	}
	want := []string{"req:a", "req:b", "req:c", "resp:c", "resp:b", "resp:a"}
	if len(log) != len(want) {
		t.Fatalf("log length = %d, want %d: %v", len(log), len(want), log)
	}
	for i := range want {
		if log[i] != want[i] {
			t.Fatalf("step %d = %q, want %q", i, log[i], want[i])
		}
	}
}

func TestEgressChainAbortsOnRequestError(t *testing.T) {
	var log []string
	errBoom := errors.New("boom")
	chain := NewEgressChain(
		recordingMW{name: "a", log: &log},
		recordingMW{name: "b", log: &log, reqErr: errBoom},
		recordingMW{name: "c", log: &log},
	)
	req := &Request{Destination: "example.com:443"}
	_, failed, err := chain.ProcessRequest(context.Background(), req)
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want %v", err, errBoom)
	}
	if failed == nil || failed.Name() != "b" {
		t.Fatalf("failing middleware should be b, got %v", failed)
	}
	// "c" must not have run.
	for _, entry := range log {
		if entry == "req:c" {
			t.Fatalf("middleware c ran despite earlier abort: %v", log)
		}
	}
}

func TestSwappableEgressChainSwap(t *testing.T) {
	var log1, log2 []string
	first := NewEgressChain(recordingMW{name: "v1", log: &log1})
	second := NewEgressChain(recordingMW{name: "v2", log: &log2})

	sc := NewSwappableEgressChain(first)
	req := &Request{Destination: "x"}
	_, _, _ = sc.ProcessRequest(context.Background(), req)

	sc.Swap(second)
	_, _, _ = sc.ProcessRequest(context.Background(), req)

	if len(log1) != 1 || log1[0] != "req:v1" {
		t.Fatalf("log1 = %v", log1)
	}
	if len(log2) != 1 || log2[0] != "req:v2" {
		t.Fatalf("log2 = %v", log2)
	}
}

func TestStaticProviderDetector(t *testing.T) {
	cases := []struct {
		host string
		want string
	}{
		{"api.openai.com", ProviderOpenAI},
		{"API.OPENAI.COM", ProviderOpenAI},
		{"api.openai.com.", ProviderOpenAI},
		{"api.anthropic.com", ProviderAnthropic},
		{"generativelanguage.googleapis.com", ProviderGoogle},
		{"aiplatform.googleapis.com", ProviderGoogle},
		{"api.cohere.ai", ProviderCohere},
		{"example.com", ProviderUnknown},
		{"nonai.local", ProviderUnknown},
	}
	d := DefaultProviderDetector()
	for _, tc := range cases {
		got := d.Detect(tc.host)
		if got != tc.want {
			t.Errorf("Detect(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
}
