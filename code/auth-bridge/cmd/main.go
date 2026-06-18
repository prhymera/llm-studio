// ─────────────────────────────────────────────────────────────
// auth-bridge — SSO proxy for Open WebUI
// Validates portal JWT, injects auth headers,
// auto-provisions users in Open WebUI.
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
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/golang-jwt/jwt/v5"
)

const (
	defaultPort = "3300"
	version     = "0.1.0"
)

// ── Configuration ────────────────────────────────────────────

type Config struct {
	Port           string
	JWTSecret      string
	OpenWebUIURL   string
	OpenWebUIAdmin string
}

func loadConfig() Config {
	return Config{
		Port:           envOrDefault("BRIDGE_PORT", defaultPort),
		JWTSecret:      envOrDefault("JWT_SECRET", "change-me-in-production"),
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

// ── JWT Claims ──────────────────────────────────────────────

type PortalClaims struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

// ── Main ─────────────────────────────────────────────────────

func main() {
	cfg := loadConfig()
	log.Printf("🔐 auth-bridge v%s starting", version)
	log.Printf("   Port:       %s", cfg.Port)
	log.Printf("   OWUI URL:   %s", cfg.OpenWebUIURL)

	// Parse Open WebUI URL for reverse proxy
	owuiURL, err := url.Parse(cfg.OpenWebUIURL)
	if err != nil {
		log.Fatalf("Invalid Open WebUI URL: %v", err)
	}

	// Create reverse proxy to Open WebUI
	proxy := httputil.NewSingleHostReverseProxy(owuiURL)

	// ── Router ──────────────────────────────────────────

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Health endpoint (no auth required)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
	})

	// All other routes go through auth check then proxy to Open WebUI
	r.HandleFunc("/*", func(w http.ResponseWriter, r *http.Request) {
		// Extract JWT from cookie or Authorization header
		tokenString := extractToken(r)
		if tokenString == "" {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		// Validate JWT
		claims, err := validateToken(tokenString, cfg.JWTSecret)
		if err != nil {
			log.Printf("Auth failed: %v", err)
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		// Auto-provision user in Open WebUI if needed
		if err := provisionUser(claims, cfg.OpenWebUIURL, cfg.OpenWebUIAdmin); err != nil {
			log.Printf("Provision warning: %v", err)
			// Non-fatal — user can still access with existing account
		}

		// Inject user headers for Open WebUI
		r.Header.Set("X-User-Id", claims.UserID)
		r.Header.Set("X-User-Email", claims.Email)
		r.Header.Set("X-User-Name", claims.Name)
		r.Header.Set("X-User-Role", claims.Role)

		// Proxy to Open WebUI
		proxy.ServeHTTP(w, r)
	})

	// ── Graceful Shutdown ───────────────────────────────

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

// ── JWT Functions ───────────────────────────────────────────

func extractToken(r *http.Request) string {
	// Check Authorization header first
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}

	// Check cookie
	if cookie, err := r.Cookie("session_jwt"); err == nil {
		return cookie.Value
	}

	return ""
}

func validateToken(tokenString, secret string) (*PortalClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &PortalClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*PortalClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	return claims, nil
}

// ── User Provisioning ───────────────────────────────────────

type OWUIUser struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Role    string `json:"role"`
}

func provisionUser(claims *PortalClaims, owuiURL, adminKey string) error {
	if adminKey == "" {
		return nil // No admin key configured, skip provisioning
	}

	// Check if user exists
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/users/-/email?email=%s", owuiURL, claims.Email), nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", adminKey))

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("check user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		// User exists — update role if needed
		var existing OWUIUser
		json.NewDecoder(resp.Body).Decode(&existing)
		mappedRole := mapRole(claims.Role)
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
		"email":    claims.Email,
		"password": generatePassword(),
		"name":     claims.Name,
		"role":     mapRole(claims.Role),
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
	case "operator":
		return "user"
	default:
		return "user"
	}
}

func generatePassword() string {
	return fmt.Sprintf("auto-%d", time.Now().UnixNano())
}
