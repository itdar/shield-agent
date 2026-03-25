package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"log/slog"

	"github.com/itdar/shield-agent/internal/config"
	"github.com/itdar/shield-agent/internal/storage"
	"github.com/itdar/shield-agent/internal/token"
)

func setupTestAPI(t *testing.T) (*API, *http.ServeMux) {
	t.Helper()
	f, err := os.CreateTemp("", "webui-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	t.Cleanup(func() { os.Remove(path) })

	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	tokenStore := token.NewStore(db.Conn())

	api := NewAPI(APIConfig{
		DB:         db,
		TokenStore: tokenStore,
		Logger:     slog.Default(),
		GetConfig: func() config.Config {
			return config.Defaults()
		},
		ToggleMW: func(name string, enabled bool) {},
	})

	mux := http.NewServeMux()
	api.RegisterRoutes(mux)
	return api, mux
}

func loginSession(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	body := `{"password":"admin"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	for _, c := range cookies {
		if c.Name == "shield_session" {
			return c.Value
		}
	}
	t.Fatal("no session cookie returned")
	return ""
}

func authedRequest(method, path, body, session string) *http.Request {
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	req.AddCookie(&http.Cookie{Name: "shield_session", Value: session})
	return req
}

func TestLogin_DefaultPassword(t *testing.T) {
	_, mux := setupTestAPI(t)

	// Login with default password.
	body := `{"password":"admin"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["force_change"] != true {
		t.Error("expected force_change=true for default password")
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	_, mux := setupTestAPI(t)

	body := `{"password":"wrong"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestChangePassword(t *testing.T) {
	_, mux := setupTestAPI(t)
	session := loginSession(t, mux)

	// Change password.
	req := authedRequest(http.MethodPost, "/api/auth/change-password", `{"new_password":"newpass123"}`, session)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Login with new password.
	body := `{"password":"newpass123"}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("login with new password failed: %d", w2.Code)
	}
}

func TestRequireAuth_NoSession(t *testing.T) {
	_, mux := setupTestAPI(t)

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestDashboard(t *testing.T) {
	_, mux := setupTestAPI(t)
	session := loginSession(t, mux)

	req := authedRequest(http.MethodGet, "/api/dashboard", "", session)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["total_requests_1h"]; !ok {
		t.Error("missing total_requests_1h")
	}
	if _, ok := resp["active_tokens"]; !ok {
		t.Error("missing active_tokens")
	}
}

func TestLogs(t *testing.T) {
	_, mux := setupTestAPI(t)
	session := loginSession(t, mux)

	req := authedRequest(http.MethodGet, "/api/logs?last=10", "", session)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestTokensCRUD(t *testing.T) {
	_, mux := setupTestAPI(t)
	session := loginSession(t, mux)

	// Create token.
	createBody := `{"name":"test-token","quota_hourly":100}`
	req := authedRequest(http.MethodPost, "/api/tokens", createBody, session)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var createResp map[string]string
	json.NewDecoder(w.Body).Decode(&createResp)
	tokenID := createResp["id"]
	rawToken := createResp["token"]
	if tokenID == "" || rawToken == "" {
		t.Fatal("expected id and token in response")
	}

	// List tokens.
	req2 := authedRequest(http.MethodGet, "/api/tokens", "", session)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", w2.Code)
	}
	var tokens []map[string]interface{}
	json.NewDecoder(w2.Body).Decode(&tokens)
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(tokens))
	}

	// Get token stats.
	req3 := authedRequest(http.MethodGet, "/api/tokens/"+tokenID+"/stats", "", session)
	w3 := httptest.NewRecorder()
	mux.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("stats: expected 200, got %d: %s", w3.Code, w3.Body.String())
	}

	// Revoke token.
	req4 := authedRequest(http.MethodDelete, "/api/tokens/"+tokenID, "", session)
	w4 := httptest.NewRecorder()
	mux.ServeHTTP(w4, req4)
	if w4.Code != http.StatusOK {
		t.Fatalf("revoke: expected 200, got %d: %s", w4.Code, w4.Body.String())
	}

	// Verify revoked.
	req5 := authedRequest(http.MethodGet, "/api/tokens/"+tokenID, "", session)
	w5 := httptest.NewRecorder()
	mux.ServeHTTP(w5, req5)
	if w5.Code != http.StatusOK {
		t.Fatalf("get revoked: expected 200, got %d", w5.Code)
	}
	var revoked map[string]interface{}
	json.NewDecoder(w5.Body).Decode(&revoked)
	if revoked["active"] != false {
		t.Error("expected token to be inactive after revoke")
	}
}

func TestMiddlewares(t *testing.T) {
	_, mux := setupTestAPI(t)
	session := loginSession(t, mux)

	req := authedRequest(http.MethodGet, "/api/middlewares", "", session)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var mws []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&mws)
	if len(mws) < 3 {
		t.Fatalf("expected at least 3 middlewares, got %d", len(mws))
	}
}

func TestMiddlewareToggle(t *testing.T) {
	toggled := false
	_, mux := setupTestAPI(t)
	// Recreate with custom toggle.
	// The toggle func was already set in setupTestAPI, but let's verify the endpoint works.
	session := loginSession(t, mux)
	_ = toggled

	req := authedRequest(http.MethodPost, "/api/middlewares/guard/toggle", "", session)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["name"] != "guard" {
		t.Errorf("expected name=guard, got %v", resp["name"])
	}
}
