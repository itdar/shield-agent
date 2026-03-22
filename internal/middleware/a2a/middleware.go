package a2a

import "net/http"

// Middleware wraps an http.Handler to add behavior to A2A protocol requests.
type Middleware interface {
	WrapHandler(next http.Handler) http.Handler
}

// Chain applies a list of Middlewares in order (first = outermost wrapper).
type Chain struct {
	items []Middleware
}

// NewChain creates a new Chain from the provided middlewares.
func NewChain(items ...Middleware) *Chain {
	return &Chain{items: items}
}

// Handler wraps final with all middlewares and returns the combined http.Handler.
func (c *Chain) Handler(final http.Handler) http.Handler {
	h := final
	for i := len(c.items) - 1; i >= 0; i-- {
		h = c.items[i].WrapHandler(h)
	}
	return h
}

// responseWriter wraps http.ResponseWriter to capture status code and bytes written.
type responseWriter struct {
	http.ResponseWriter
	status int
	size   int
}

func wrapResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{ResponseWriter: w, status: http.StatusOK}
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.size += n
	return n, err
}
