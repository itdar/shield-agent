// Package webui provides the web management UI API and static file serving.
package webui

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/itdar/shield-agent/internal/config"
	"github.com/itdar/shield-agent/internal/storage"
	"github.com/itdar/shield-agent/internal/token"
)

// API provides JSON API endpoints for the web UI.
type API struct {
	db         *storage.DB
	tokenStore *token.Store
	logger     *slog.Logger
	adminDB    adminDB
	sessions   map[string]time.Time // sessionID -> expiry
	getCfg     func() config.Config
	toggleMW   func(name string, enabled bool)
}

type adminDB struct {
	db *storage.DB
}

// APIConfig holds dependencies for creating the API.
type APIConfig struct {
	DB         *storage.DB
	TokenStore *token.Store
	Logger     *slog.Logger
	GetConfig  func() config.Config
	ToggleMW   func(name string, enabled bool)
}

// NewAPI creates a new API handler.
func NewAPI(cfg APIConfig) *API {
	api := &API{
		db:         cfg.DB,
		tokenStore: cfg.TokenStore,
		logger:     cfg.Logger,
		adminDB:    adminDB{db: cfg.DB},
		sessions:   make(map[string]time.Time),
		getCfg:     cfg.GetConfig,
		toggleMW:   cfg.ToggleMW,
	}
	// Ensure default admin password exists.
	api.ensureAdminPassword()
	return api
}

// RegisterRoutes registers API routes on the given mux.
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/auth/login", a.handleLogin)
	mux.HandleFunc("/api/auth/change-password", a.requireAuth(a.handleChangePassword))
	mux.HandleFunc("/api/dashboard", a.requireAuth(a.handleDashboard))
	mux.HandleFunc("/api/logs", a.requireAuth(a.handleLogs))
	mux.HandleFunc("/api/tokens", a.requireAuth(a.handleTokens))
	mux.HandleFunc("/api/tokens/", a.requireAuth(a.handleTokenByID))
	mux.HandleFunc("/api/middlewares", a.requireAuth(a.handleMiddlewares))
	mux.HandleFunc("/api/middlewares/", a.requireAuth(a.handleMiddlewareToggle))
	mux.HandleFunc("/api/keys", a.requireAuth(a.handleKeys))
	mux.HandleFunc("/api/keys/", a.requireAuth(a.handleKeyByID))
}

// --- Auth ---

func (a *API) ensureAdminPassword() {
	conn := a.db.Conn()
	var count int
	row := conn.QueryRow("SELECT COUNT(*) FROM admin_config WHERE key = 'password_hash'")
	if err := row.Scan(&count); err != nil || count == 0 {
		hash, _ := bcrypt.GenerateFromPassword([]byte("admin"), bcrypt.DefaultCost)
		conn.Exec("INSERT OR REPLACE INTO admin_config (key, value) VALUES ('password_hash', ?)", string(hash))
		conn.Exec("INSERT OR REPLACE INTO admin_config (key, value) VALUES ('force_change', 'true')")
	}
}

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	conn := a.db.Conn()
	var hash string
	if err := conn.QueryRow("SELECT value FROM admin_config WHERE key = 'password_hash'").Scan(&hash); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Password)); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid password"})
		return
	}

	// Check if password change is required.
	var forceChange string
	conn.QueryRow("SELECT value FROM admin_config WHERE key = 'force_change'").Scan(&forceChange)

	sessionID := generateSessionID()
	a.sessions[sessionID] = time.Now().Add(24 * time.Hour)

	http.SetCookie(w, &http.Cookie{
		Name:     "shield_session",
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   86400,
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":           true,
		"force_change": forceChange == "true",
	})
}

func (a *API) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.NewPassword == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "new_password required"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(body.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	conn := a.db.Conn()
	conn.Exec("INSERT OR REPLACE INTO admin_config (key, value) VALUES ('password_hash', ?)", string(hash))
	conn.Exec("INSERT OR REPLACE INTO admin_config (key, value) VALUES ('force_change', 'false')")

	writeJSON(w, http.StatusOK, map[string]string{"ok": "password changed"})
}

func (a *API) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("shield_session")
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
			return
		}
		expiry, ok := a.sessions[cookie.Value]
		if !ok || time.Now().After(expiry) {
			delete(a.sessions, cookie.Value)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "session expired"})
			return
		}
		next(w, r)
	}
}

// --- Dashboard ---

func (a *API) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	logs, _ := a.db.QueryLogs(storage.QueryOptions{Last: 1000, Since: time.Hour})
	tokens, _ := a.tokenStore.List(true)

	var totalReqs, errorCount int
	var totalLatency float64
	for _, l := range logs {
		totalReqs++
		if !l.Success {
			errorCount++
		}
		totalLatency += l.LatencyMs
	}

	avgLatency := 0.0
	errorRate := 0.0
	if totalReqs > 0 {
		avgLatency = totalLatency / float64(totalReqs)
		errorRate = float64(errorCount) / float64(totalReqs) * 100
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total_requests":    totalReqs,
		"total_requests_1h": totalReqs,
		"error_rate":        errorRate / 100, // fraction 0.0-1.0 for JS (* 100)
		"avg_latency_ms":    avgLatency,
		"active_tokens":     len(tokens),
	})
}

// --- Logs ---

func (a *API) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	opts := storage.QueryOptions{Last: 50}
	if v := r.URL.Query().Get("last"); v != "" {
		fmt.Sscanf(v, "%d", &opts.Last)
	}
	if v := r.URL.Query().Get("method"); v != "" {
		opts.Method = v
	}
	if v := r.URL.Query().Get("agent"); v != "" {
		opts.AgentHash = v
	}
	if v := r.URL.Query().Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			opts.Since = d
		}
	}

	logs, err := a.db.QueryLogs(opts)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, logs)
}

// --- Tokens ---

func (a *API) handleTokens(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		tokens, err := a.tokenStore.List(true)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if tokens == nil {
			tokens = []token.Token{}
		}
		writeJSON(w, http.StatusOK, tokens)

	case http.MethodPost:
		var body struct {
			Name           string   `json:"name"`
			QuotaHourly    int      `json:"quota_hourly"`
			QuotaMonthly   int      `json:"quota_monthly"`
			ExpiresIn      string   `json:"expires_in"` // e.g. "720h"
			AllowedMethods []string `json:"allowed_methods"`
			IPAllowlist    []string `json:"ip_allowlist"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
			return
		}

		raw, err := token.GenerateToken()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "token generation failed"})
			return
		}
		hash := token.HashToken(raw)

		var expiresAt *time.Time
		if body.ExpiresIn != "" {
			if d, err := parseDuration(body.ExpiresIn); err == nil {
				t := time.Now().Add(d)
				expiresAt = &t
			}
		}

		id, err := a.tokenStore.Create(body.Name, hash, expiresAt, body.QuotaHourly, body.QuotaMonthly, body.AllowedMethods, body.IPAllowlist)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusCreated, map[string]string{
			"id":    id,
			"token": raw,
			"note":  "Save this token now. It will not be shown again.",
		})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *API) handleTokenByID(w http.ResponseWriter, r *http.Request) {
	// Extract ID from path: /api/tokens/{id} or /api/tokens/{id}/stats
	path := strings.TrimPrefix(r.URL.Path, "/api/tokens/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "token ID required", http.StatusBadRequest)
		return
	}
	id := parts[0]

	// /api/tokens/{id}/stats
	if len(parts) >= 2 && parts[1] == "stats" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		since := 24 * time.Hour
		if v := r.URL.Query().Get("since"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				since = d
			}
		}
		stats, err := a.tokenStore.GetStats(id, since)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, stats)
		return
	}

	// DELETE /api/tokens/{id}
	if r.Method == http.MethodDelete {
		if err := a.tokenStore.Revoke(id); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "token revoked"})
		return
	}

	// GET /api/tokens/{id}
	if r.Method == http.MethodGet {
		tok, err := a.tokenStore.GetByID(id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if tok == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, http.StatusOK, tok)
		return
	}

	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// --- Middlewares ---

func (a *API) handleMiddlewares(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := a.getCfg()
	type mwStatus struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	var result []mwStatus
	for _, entry := range cfg.Middlewares {
		enabled := entry.Enabled == nil || *entry.Enabled
		result = append(result, mwStatus{Name: entry.Name, Enabled: enabled})
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) handleMiddlewareToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// /api/middlewares/{name}/toggle
	path := strings.TrimPrefix(r.URL.Path, "/api/middlewares/")
	name := strings.TrimSuffix(path, "/toggle")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "middleware name required"})
		return
	}

	// Toggle: find current state and flip.
	cfg := a.getCfg()
	currentEnabled := true
	for _, entry := range cfg.Middlewares {
		if entry.Name == name {
			currentEnabled = entry.Enabled == nil || *entry.Enabled
			break
		}
	}

	if a.toggleMW != nil {
		a.toggleMW(name, !currentEnabled)
	}

	// Persist middleware state to DB so it survives restarts.
	if a.db != nil {
		key := fmt.Sprintf("middleware_enabled_%s", name)
		val := "false"
		if !currentEnabled {
			val = "true"
		}
		if err := a.db.SaveConfig(key, val); err != nil {
			a.logger.Error("failed to persist middleware toggle", "error", err.Error())
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name":    name,
		"enabled": !currentEnabled,
	})
}

// --- Agent Keys ---

func (a *API) handleKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		keys, err := a.db.ListAgentKeys(false)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if keys == nil {
			keys = []storage.AgentKey{}
		}
		writeJSON(w, http.StatusOK, keys)

	case http.MethodPost:
		var body struct {
			ID        string `json:"id"`
			PublicKey string `json:"public_key"`
			Label     string `json:"label"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" || body.PublicKey == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id and public_key are required"})
			return
		}
		// Validate base64 and key size.
		raw, err := base64.StdEncoding.DecodeString(body.PublicKey)
		if err != nil || len(raw) != 32 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "public_key must be base64-encoded 32-byte Ed25519 key"})
			return
		}
		if err := a.db.InsertAgentKey(body.ID, body.PublicKey, body.Label); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "agent ID already exists"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"ok": "key registered", "id": body.ID})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *API) handleKeyByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/keys/")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key ID required"})
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.db.DeleteAgentKey(id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "key deleted"})
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data) //nolint:errcheck
}

func generateSessionID() string {
	b := make([]byte, 32)
	rand.Read(b) //nolint:errcheck
	return hex.EncodeToString(b)
}

var durationRe = regexp.MustCompile(`(?i)^(\d+)\s*([dhms])$`)

// parseDuration parses duration strings like "24h", "7d", "30m", "60s" (case-insensitive).
func parseDuration(s string) (time.Duration, error) {
	m := durationRe.FindStringSubmatch(strings.TrimSpace(s))
	if m != nil {
		n, _ := strconv.Atoi(m[1])
		switch strings.ToLower(m[2]) {
		case "d":
			return time.Duration(n) * 24 * time.Hour, nil
		case "h":
			return time.Duration(n) * time.Hour, nil
		case "m":
			return time.Duration(n) * time.Minute, nil
		case "s":
			return time.Duration(n) * time.Second, nil
		}
	}
	return time.ParseDuration(s)
}
