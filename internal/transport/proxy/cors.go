package proxy

import "net/http"

// SetCORSHeaders writes CORS response headers based on allowed origins.
// If allowedOrigins is empty, no headers are written.
// If "*" is in the list, all origins are allowed and "*" is set as the header value.
// Otherwise, the request Origin is matched against the list.
func SetCORSHeaders(w http.ResponseWriter, r *http.Request, allowedOrigins []string) {
	origin := r.Header.Get("Origin")
	if len(allowedOrigins) == 0 {
		return
	}

	for _, allowed := range allowedOrigins {
		if allowed == "*" || allowed == origin {
			w.Header().Set("Access-Control-Allow-Origin", allowed)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Session-Id")
			w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, DELETE")
			return
		}
	}
}
