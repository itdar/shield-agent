package process

import (
	"bytes"
	"errors"
	"testing"
)

// errWriter always returns an error on Write.
type errWriter struct{ err error }

func (e *errWriter) Write(_ []byte) (int, error) { return 0, e.err }

func TestInterceptorCopiesBytes(t *testing.T) {
	src := bytes.NewReader([]byte("hello world"))
	var dst bytes.Buffer
	out := make(chan []byte, 16)

	err := Interceptor(src, &dst, out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dst.String() != "hello world" {
		t.Errorf("dst got %q, want %q", dst.String(), "hello world")
	}
}

func TestInterceptorSendsToObserver(t *testing.T) {
	data := []byte("observe me")
	src := bytes.NewReader(data)
	var dst bytes.Buffer
	out := make(chan []byte, 16)

	if err := Interceptor(src, &dst, out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Channel should be closed now; drain it.
	var received []byte
	for chunk := range out {
		received = append(received, chunk...)
	}

	if !bytes.Equal(received, data) {
		t.Errorf("observer got %q, want %q", received, data)
	}
}

func TestInterceptorEOF(t *testing.T) {
	src := bytes.NewReader([]byte{}) // empty — immediate EOF
	var dst bytes.Buffer
	out := make(chan []byte, 16)

	err := Interceptor(src, &dst, out)
	if err != nil {
		t.Fatalf("expected nil error on EOF, got: %v", err)
	}

	// Channel must be closed.
	_, open := <-out
	if open {
		t.Error("expected observer channel to be closed after EOF")
	}
}

func TestInterceptorWriteError(t *testing.T) {
	src := bytes.NewReader([]byte("data"))
	wantErr := errors.New("write failed")
	dst := &errWriter{err: wantErr}
	out := make(chan []byte, 16)

	err := Interceptor(src, dst, out)
	if err == nil {
		t.Fatal("expected write error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("got error %v, want %v", err, wantErr)
	}
}
