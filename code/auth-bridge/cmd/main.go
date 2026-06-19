// ─────────────────────────────────────────────────────────────
// auth-bridge — SSO proxy for Open WebUI
// Validates portal session by calling backend's /auth/me endpoint,
// injects auth headers, auto-provisions users in Open WebUI.
// Part of the Frao Technologies LLM Studio
// ─────────────────────────────────────────────────────────────

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const (
	defaultPort  = "3300"
	version      = "0.1.0"
)

// ── Configuration ────────────────────────────────────────────

type Config struct {
	Port           string
	BackendURL     string // Portal backend URL to validate sessions
	OpenWebUIURL   string
	OpenWebUIAdmin string
}

func loadConfig() Config {
	return Config{
		Port:           envOrDefault("BRIDGE_PORT", defaultPort),
		BackendURL:     envOrDefault("BACKEND_URL", "http://backend:8080"),
		OpenWebUIURL:   envOrDefault("OPEN_WEBUI_URL", "http://open-webui:8080"),
		OpenWebUIAdmin: envOrDefault("OPEN_WEBUI_ADMIN_KEY", ""),
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ── User Info (from backend /auth/me) ────────────────────────

type UserInfo struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Name     string `json:"full_name,omitempty"`
	FullName string `json:"full_name,omitempty"`
	Role     string `json:"role"`
}

func (u *UserInfo) displayName() string {
	if u.Name != "" {
		return u.Name
	}
	if u.FullName != "" {
		return u.FullName
	}
	return u.Email
}

// ── Main ─────────────────────────────────────────────────────

func main() {
	cfg := loadConfig()
	log.Printf("🔐 auth-bridge v%s starting", version)
	log.Printf("   Port:     %s", cfg.Port)
	log.Printf("   Backend:  %s", cfg.BackendURL)
	log.Printf("   OWUI:     %s", cfg.OpenWebUIURL)

	owuiURL, err := url.Parse(cfg.OpenWebUIURL)
	if err != nil {
		log.Fatalf("Invalid Open WebUI URL: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(owuiURL)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
	})

	r.HandleFunc("/*", func(w http.ResponseWriter, r *http.Request) {
		// Validate session by calling the portal backend
		user, err := validateSession(r, cfg.BackendURL)
		if err != nil {
			log.Printf("Session validation failed: %v", err)
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		// Auto-provision user in Open WebUI
		if err := provisionUser(user, cfg.OpenWebUIURL, cfg.OpenWebUIAdmin); err != nil {
			log.Printf("Provision warning: %v", err)
		}

		// Inject auth headers for Open WebUI
		r.Header.Set("X-User-Id", user.ID)
		r.Header.Set("X-User-Email", user.Email)
		r.Header.Set("X-User-Name", user.displayName())
		r.Header.Set("X-User-Role", user.Role)

		proxy.ServeHTTP(w, r)
	})

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Shutting down...")
		os.Exit(0)
	}()

	addr := fmt.Sprintf(":%s", cfg.Port)
	log.Printf("Listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// ── Session Validation ───────────────────────────────────────

func validateSession(r *http.Request, backendURL string) (*UserInfo, error) {
	// Forward the request's cookies to the backend's /auth/me endpoint
	meURL := fmt.Sprintf("%s/api/v1/auth/me", backendURL)

	req, err := http.NewRequest("GET", meURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Copy cookies from the original request
	for _, c := range r.Cookies() {
		req.AddCookie(c)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("backend request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("unauthorized")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("backend returned %d", resp.StatusCode)
	}

	var user UserInfo
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("decode user: %w", err)
	}

	if user.ID == "" {
		return nil, fmt.Errorf("empty user id")
	}

	return &user, nil
}

// ── User Provisioning ───────────────────────────────────────

func provisionUser(user *UserInfo, owuiURL, adminKey string) error {
	if adminKey == "" {
		return nil
	}

	client := &http.Client{Timeout: 5 * time.Second}
	email := user.Email

	// Check if user exists
	checkReq, _ := http.NewRequest("GET",
		fmt.Sprintf("%s/api/v1/users/-/email?email=%s", owuiURL, url.QueryEscape(email)), nil)
	checkReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", adminKey))

	resp, err := client.Do(checkReq)
	if err != nil {
		return fmt.Errorf("check user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		// User exists — update role if needed
		var existing struct {
			ID   string `json:"id"`
			Role string `json:"role"`
		}
		json.NewDecoder(resp.Body).Decode(&existing)
		mappedRole := mapRole(user.Role)
		if existing.Role != mappedRole {
			updateReq, _ := http.NewRequest("POST",
				fmt.Sprintf("%s/api/v1/users/%s/role", owuiURL, existing.ID),
				bytes.NewBuffer([]byte(fmt.Sprintf(`{"role":"%s"}`, mappedRole))))
			updateReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", adminKey))
			updateReq.Header.Set("Content-Type", "application/json")
			client.Do(updateReq)
		}
		return nil
	}

	// Create new user
	body, _ := json.Marshal(map[string]string{
		"email":    email,
		"password": fmt.Sprintf("auto-%d", time.Now().UnixNano()),
		"name":     user.displayName(),
		"role":     mapRole(user.Role),
	})

	createReq, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/users", owuiURL),
		bytes.NewBuffer(body))
	createReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", adminKey))
	createReq.Header.Set("Content-Type", "application/json")

	resp, err = client.Do(createReq)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create user failed (%d): %s", resp.StatusCode, string(body))
	}

	return nil
}

func mapRole(portalRole string) string {
	switch portalRole {
	case "superadmin", "admin":
		return "admin"
	default:
		return "user"
	}
}
