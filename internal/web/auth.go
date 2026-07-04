package web

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const authCookieName = "kscribe_token"

// loginAttemptLimit / loginAttemptWindow bound failed login attempts per
// client IP. RemoteAddr is used directly — X-Forwarded-For is deliberately
// ignored since it is spoofable without a trusted proxy in front.
const (
	loginAttemptLimit  = 10
	loginAttemptWindow = time.Minute
	loginTrackedIPsMax = 1024 // ponytail: full reset when exceeded; an attacker churning IPs past this only resets budgets, never gains extra attempts within a window
)

// loginLimiter is a sliding window over failed login attempts, per client IP.
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

// tooMany reports whether ip's failed-attempt budget is exhausted.
func (l *loginLimiter) tooMany(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-loginAttemptWindow)
	keep := l.failures[ip][:0]
	for _, t := range l.failures[ip] {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(l.failures, ip)
	} else {
		l.failures[ip] = keep
	}
	return len(keep) >= loginAttemptLimit
}

func (l *loginLimiter) recordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failures == nil || len(l.failures) > loginTrackedIPsMax {
		l.failures = make(map[string][]time.Time)
	}
	l.failures[ip] = append(l.failures[ip], time.Now())
}

// clientIP extracts the host part of RemoteAddr.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// tokenMatches compares a candidate against the configured token in constant
// time (SEC-001).
func (s *Server) tokenMatches(candidate string) bool {
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(s.authToken)) == 1
}

// authorized reports whether the request carries valid credentials — either
// an Authorization: Bearer header (API/curl) or the session cookie (browser).
func (s *Server) authorized(r *http.Request) bool {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if s.tokenMatches(strings.TrimPrefix(h, "Bearer ")) {
			return true
		}
	}
	if c, err := r.Cookie(authCookieName); err == nil && s.tokenMatches(c.Value) {
		return true
	}
	return false
}

// requireAuth is chi middleware guarding the dashboard. Browser page loads
// redirect to /login; API, SSE, and asset requests get a plain 401.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authorized(r) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet &&
			strings.Contains(r.Header.Get("Accept"), "text/html") &&
			!strings.HasSuffix(r.URL.Path, "/stream") {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

// ponytail: inline HTML login form — no templ component for a single static page.
const loginPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>kscribe — Login</title>
<style>
body{font-family:system-ui,sans-serif;display:grid;place-items:center;min-height:100vh;margin:0;background:#0b1120;color:#e2e8f0}
form{background:#1e293b;padding:2rem;border-radius:8px;display:grid;gap:.75rem;min-width:280px}
input,button{padding:.5rem .75rem;border-radius:4px;border:1px solid #334155;font-size:1rem}
input{background:#0f172a;color:inherit}
button{background:#3b82f6;color:#fff;border:none;cursor:pointer}
.err{color:#f87171;font-size:.875rem;margin:0}
</style></head><body>
<form method="post" action="/login">
<h1 style="margin:0;font-size:1.25rem">kscribe</h1>
%s<input type="password" name="token" placeholder="Access token" autofocus autocomplete="current-password">
<button type="submit">Sign in</button>
</form></body></html>`

func (s *Server) loginForm(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(strings.Replace(loginPage, "%s", "", 1)))
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if s.loginAttempts.tooMany(clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many login attempts; try again later", http.StatusTooManyRequests)
		return
	}
	_ = r.ParseForm()
	if !s.tokenMatches(r.FormValue("token")) {
		s.loginAttempts.recordFailure(clientIP(r))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(strings.Replace(loginPage, "%s", `<p class="err">Invalid token</p>`, 1)))
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    r.FormValue("token"),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Secure omitted: TLS termination is deployment-specific (Ingress).
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
