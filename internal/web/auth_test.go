package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/amjadjibon/kscribe/internal/web"
)

const testToken = "s3cret-token"

func newAuthServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := web.New(seedStore(), web.NewBroker(), nil).WithAuthToken(testToken)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// noRedirect returns a client that does not follow redirects, so 303s are observable.
func noRedirect() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func TestAuthDisabledByDefault(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("no token configured: GET / = %d, want 200", resp.StatusCode)
	}
}

func TestAuthRequired(t *testing.T) {
	ts := newAuthServer(t)

	// Bare API request → 401.
	resp, err := http.Get(ts.URL + "/incidents/default/x/stream")
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated SSE = %d, want 401", resp.StatusCode)
	}

	// Browser page load → redirect to /login.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.Header.Set("Accept", "text/html")
	resp, err = noRedirect().Do(req)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Errorf("browser GET / = %d → %q, want 303 → /login", resp.StatusCode, resp.Header.Get("Location"))
	}

	// /healthz always open.
	resp, err = http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200", resp.StatusCode)
	}
}

func TestAuthBearer(t *testing.T) {
	ts := newAuthServer(t)

	for _, tc := range []struct {
		token string
		want  int
	}{
		{testToken, http.StatusOK},
		{"wrong", http.StatusUnauthorized},
	} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("bearer %q: GET / = %d, want %d", tc.token, resp.StatusCode, tc.want)
		}
	}
}

func TestLoginFlow(t *testing.T) {
	ts := newAuthServer(t)

	// Wrong token → 401 with form re-shown.
	resp, err := noRedirect().PostForm(ts.URL+"/login", url.Values{"token": {"nope"}})
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token login = %d, want 401", resp.StatusCode)
	}

	// Correct token → 303 with cookie; cookie then grants access.
	resp, err = noRedirect().PostForm(ts.URL+"/login", url.Values{"token": {testToken}})
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login = %d, want 303", resp.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "kscribe_token" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("login did not set kscribe_token cookie")
	}
	if !cookie.HttpOnly {
		t.Error("cookie is not HttpOnly")
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.AddCookie(cookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET / with cookie: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("cookie GET / = %d, want 200", resp.StatusCode)
	}
}

func TestLoginFormServed(t *testing.T) {
	ts := newAuthServer(t)

	resp, err := http.Get(ts.URL + "/login")
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /login = %d, want 200", resp.StatusCode)
	}
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), `name="token"`) {
		t.Error("login page does not contain the token form field")
	}
}

// TestLoginThrottled verifies the failed-attempt window returns 429 once
// exhausted, including for subsequent correct-token attempts.
func TestLoginThrottled(t *testing.T) {
	ts := newAuthServer(t)

	for i := 0; i < 10; i++ {
		resp, err := noRedirect().PostForm(ts.URL+"/login", url.Values{"token": {"wrong"}})
		if err != nil {
			t.Fatalf("POST /login #%d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i, resp.StatusCode)
		}
	}

	resp, err := noRedirect().PostForm(ts.URL+"/login", url.Values{"token": {testToken}})
	if err != nil {
		t.Fatalf("POST /login after exhaustion: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("login after 10 failures = %d, want 429", resp.StatusCode)
	}
}
