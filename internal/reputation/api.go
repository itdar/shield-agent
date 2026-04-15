package reputation

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// API handles HTTP endpoints for the reputation system.
type API struct {
	provider *LocalProvider
	logger   *slog.Logger
}

// NewAPI creates a reputation API handler.
func NewAPI(provider *LocalProvider, logger *slog.Logger) *API {
	return &API{provider: provider, logger: logger}
}

// RegisterAPI registers reputation HTTP endpoints on the given mux.
func RegisterAPI(mux *http.ServeMux, api *API) {
	mux.HandleFunc("/api/reputation/stats", api.handleStats)
	mux.HandleFunc("/api/reputation/report", api.handleReport)
	mux.HandleFunc("/api/reputation/recalculate", api.handleRecalculate)
	mux.HandleFunc("/api/reputation/", api.handleAgent)
	mux.HandleFunc("/api/reputation", api.handleList)
}

// handleList returns all agent reputation scores.
func (a *API) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	scores, err := a.provider.ListScores(r.Context())
	if err != nil {
		a.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if scores == nil {
		scores = []Score{}
	}
	a.jsonOK(w, scores)
}

// handleAgent returns the reputation score for a single agent.
func (a *API) handleAgent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	hash := strings.TrimPrefix(r.URL.Path, "/api/reputation/")
	if hash == "" {
		a.jsonError(w, "agent hash required", http.StatusBadRequest)
		return
	}

	score, err := a.provider.GetScore(r.Context(), hash)
	if err != nil {
		a.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if score == nil {
		a.jsonError(w, "agent not found", http.StatusNotFound)
		return
	}
	a.jsonOK(w, score)
}

// handleStats returns aggregate reputation statistics.
func (a *API) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stats, err := a.provider.Stats(r.Context())
	if err != nil {
		a.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.jsonOK(w, stats)
}

// reportRequest is the body for POST /api/reputation/report.
type reportRequest struct {
	InstanceID string  `json:"instance_id"`
	Scores     []Score `json:"scores"`
}

// handleReport accepts reputation scores from a remote shield-agent instance.
func (a *API) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req reportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		a.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.Scores) == 0 {
		a.jsonError(w, "no scores provided", http.StatusBadRequest)
		return
	}

	accepted, err := a.provider.ReportScores(r.Context(), req.InstanceID, req.Scores)
	if err != nil {
		a.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	a.logger.Info("received remote reputation report",
		slog.String("instance", req.InstanceID),
		slog.Int("accepted", accepted),
	)
	a.jsonOK(w, map[string]int{"accepted": accepted})
}

// handleRecalculate triggers an immediate score recalculation.
func (a *API) handleRecalculate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.provider.Recalculate(); err != nil {
		a.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.jsonOK(w, map[string]string{"status": "ok"})
}

func (a *API) jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func (a *API) jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}
