// ─────────────────────────────────────────────────────────────
// auth-bridge — SSO proxy for Open WebUI
// Validates portal session by calling backend's /auth/me endpoint,
// injects auth headers, auto-provisions users in Open WebUI,
// then auto-signs in via trusted headers so the user lands
// already authenticated (no login form shown).
// Part of the Frao Technologies LLM Studio
// ─────────────────────────────────────────────────────────────

package main

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	_ "modernc.org/sqlite"
)

const (
	defaultPort = "3300"
	version     = "0.3.0"
)

// ── Configuration ────────────────────────────────────────────

type Config struct {
	Port         string
	BackendURL   string
	OpenWebUIURL string
	OWUIDBPath   string
}

func loadConfig() Config {
	return Config{
		Port:         envOrDefault("BRIDGE_PORT", defaultPort),
		BackendURL:   envOrDefault("BACKEND_URL", "http://backend:8080"),
		OpenWebUIURL: envOrDefault("OPEN_WEBUI_URL", "http://open-webui:8080"),
		OWUIDBPath:   envOrDefault("OWUI_DB_PATH", "/owui-data/webui.db"),
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

// ── Token Cookie Cache ───────────────────────────────────────
// Caches the signin token cookie per user ID so we don't call the
// signin endpoint on every single request.

var (
	tokenCache   = make(map[string]*http.Cookie)
	tokenCacheMu sync.RWMutex
)

func getCachedToken(userID string) *http.Cookie {
	tokenCacheMu.RLock()
	defer tokenCacheMu.RUnlock()
	return tokenCache[userID]
}

func setCachedToken(userID string, cookie *http.Cookie) {
	tokenCacheMu.Lock()
	defer tokenCacheMu.Unlock()
	tokenCache[userID] = cookie
}

// ── Main ─────────────────────────────────────────────────────

func main() {
	cfg := loadConfig()
	log.Printf("🔐 auth-bridge v%s starting", version)
	log.Printf("   Port:     %s", cfg.Port)
	log.Printf("   Backend:  %s", cfg.BackendURL)
	log.Printf("   OWUI:     %s", cfg.OpenWebUIURL)
	log.Printf("   DB Path:  %s", cfg.OWUIDBPath)

	owuiURL, err := url.Parse(cfg.OpenWebUIURL)
	if err != nil {
		log.Fatalf("Invalid Open WebUI URL: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(owuiURL)
	// Do not buffer — needed for streaming responses
	proxy.FlushInterval = -1

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

		// Auto-provision user in Open WebUI via direct SQLite write
		if err := provisionUserDB(user, cfg.OWUIDBPath); err != nil {
			log.Printf("Provision warning: %v", err)
		}

		// Inject auth headers for Open WebUI
		r.Header.Set("X-User-Id", user.ID)
		r.Header.Set("X-User-Email", user.Email)
		r.Header.Set("X-User-Name", user.displayName())
		r.Header.Set("X-User-Role", user.Role)

		// Auto-signin: call Open WebUI's signin endpoint with trusted headers
		// to obtain a session JWT cookie. Then inject it via a response wrapper
		// so the browser has an active session immediately.
		tokenCookie := getCachedToken(user.ID)
		if tokenCookie == nil {
			tokenCookie = owuiSignin(user, cfg.OpenWebUIURL)
			if tokenCookie != nil {
				setCachedToken(user.ID, tokenCookie)
				log.Printf("Auto-signed in user %s", user.Email)
			} else {
				log.Printf("Signin failed for %s — user may need to click sign in", user.Email)
			}
		}

		// Wrap the response writer to inject the token cookie
		if tokenCookie != nil {
			w = &cookieInjector{ResponseWriter: w, cookie: tokenCookie}
		}

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

// ── Open WebUI Auto-Signin ───────────────────────────────────
// Calls the Open WebUI signin endpoint with trusted X-User-* headers.
// Returns the Set-Cookie from the response so the browser gets a session.

func owuiSignin(user *UserInfo, owuiURL string) *http.Cookie {
	signinURL := fmt.Sprintf("%s/api/v1/auths/signin", owuiURL)

	// Build a minimal signin body — content doesn't matter when trusted headers are used
	bodyJSON, _ := json.Marshal(map[string]string{
		"email":    user.Email,
		"password": "auto-sso-placeholder",
	})

	req, err := http.NewRequest("POST", signinURL, bytes.NewReader(bodyJSON))
	if err != nil {
		log.Printf("Signin: create request error: %v", err)
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Email", user.Email)
	req.Header.Set("X-User-Name", user.displayName())
	req.Header.Set("X-User-Role", user.Role)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Signin: request error: %v", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Read body for debugging
		var bodyBytes [256]byte
		n, _ := resp.Body.Read(bodyBytes[:])
		log.Printf("Signin: unexpected status %d: %s", resp.StatusCode, string(bodyBytes[:n]))
		return nil
	}

	// Extract the token cookie from the response
	for _, c := range resp.Cookies() {
		if c.Name == "token" {
			return c
		}
	}

	log.Printf("Signin: no token cookie in response")
	return nil
}

// ── Session Validation ───────────────────────────────────────

func validateSession(r *http.Request, backendURL string) (*UserInfo, error) {
	meURL := fmt.Sprintf("%s/api/v1/auth/me", backendURL)

	req, err := http.NewRequest("GET", meURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Copy ALL cookies from the original request (includes the portal's "id" cookie)
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

// ── User Provisioning (Direct SQLite) ────────────────────────

func provisionUserDB(user *UserInfo, dbPath string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("set WAL mode: %w", err)
	}

	// Check if user exists by email
	var existingID string
	var existingRole string
	err = db.QueryRow("SELECT id, COALESCE(role,'user') FROM \"user\" WHERE email = ?", user.Email).Scan(&existingID, &existingRole)
	if err == nil {
		mappedRole := mapRole(user.Role)
		if existingRole != mappedRole {
			now := time.Now().Unix()
			if _, err := db.Exec("UPDATE \"user\" SET role = ?, updated_at = ? WHERE id = ?", mappedRole, now, existingID); err != nil {
				return fmt.Errorf("update role: %w", err)
			}
			log.Printf("Updated user %s role: %s -> %s", user.Email, existingRole, mappedRole)
		}
		return nil
	}

	// User doesn't exist — create them
	now := time.Now().Unix()
	mappedRole := mapRole(user.Role)
	placeHolderHash := "$2b$12$placeholderplaceholderplaceholderplaceholderplaceholderpl"

	_, err = db.Exec(
		`INSERT INTO "user" (id, name, email, role, last_active_at, updated_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		user.ID, user.displayName(), user.Email, mappedRole, now, now, now,
	)
	if err != nil {
		return fmt.Errorf("insert user: %w", err)
	}

	_, err = db.Exec(
		`INSERT OR IGNORE INTO auth (id, email, password, active)
		 VALUES (?, ?, ?, ?)`,
		user.ID, user.Email, placeHolderHash, 1,
	)
	if err != nil {
		return fmt.Errorf("insert auth: %w", err)
	}

	log.Printf("Provisioned new user: %s (%s) as %s", user.Email, user.displayName(), mappedRole)
	return nil
}

func mapRole(portalRole string) string {
	switch portalRole {
	case "superadmin", "super_admin", "admin":
		return "admin"
	default:
		return "user"
	}
}

// ── Cookie Injector Response Writer ──
// Wraps http.ResponseWriter to inject a Set-Cookie header into the response
// before it's written to the client. This avoids the race condition of using
// ModifyResponse on a shared reverse proxy.

type cookieInjector struct {
	http.ResponseWriter
	cookie    *http.Cookie
	cookieSet bool
}

func (c *cookieInjector) WriteHeader(statusCode int) {
	if !c.cookieSet {
		c.Header().Add("Set-Cookie", c.cookie.String())
		c.cookieSet = true
	}
	c.ResponseWriter.WriteHeader(statusCode)
}

func (c *cookieInjector) Write(data []byte) (int, error) {
	if !c.cookieSet {
		c.Header().Add("Set-Cookie", c.cookie.String())
		c.cookieSet = true
	}
	return c.ResponseWriter.Write(data)
}

// Hijack implements http.Hijacker so WebSocket connections work through the proxy.
// Delegates to the underlying ResponseWriter if it supports hijacking.
func (c *cookieInjector) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := c.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("cookieInjector: underlying ResponseWriter does not implement http.Hijacker")
	}
	return hijacker.Hijack()
}


