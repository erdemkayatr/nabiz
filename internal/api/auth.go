package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/erdemkayatr/nabiz/internal/identity"
)

// SessionCookie is the name of the session cookie.
const SessionCookie = "nabiz_session"

// SessionTTL is how long a session lives.
const SessionTTL = 12 * time.Hour

type ctxKey int

const (
	ctxAccess ctxKey = iota
	ctxToken
)

// accessFrom returns the authorization context attached to a request.
func accessFrom(r *http.Request) *identity.Access {
	a, _ := r.Context().Value(ctxAccess).(*identity.Access)
	return a
}

// --- login attempt limiter ---

// loginLimiter caps password attempts coming from the same source.
//
// Deliberately not a persistent store: the point is to make brute force
// expensive, not to build a perfect defence. The counter resets when the
// process restarts, and that is acceptable.
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]*attemptWindow
	max      int
	window   time.Duration
}

type attemptWindow struct {
	count int
	reset time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{attempts: map[string]*attemptWindow{}, max: max, window: window}
}

// allow says whether the attempt is permitted, and increments the counter.
func (l *loginLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	w, ok := l.attempts[key]
	if !ok || now.After(w.reset) {
		l.attempts[key] = &attemptWindow{count: 1, reset: now.Add(l.window)}
		l.sweep(now)
		return true
	}
	w.count++
	return w.count <= l.max
}

// clear resets the counter after a successful login.
func (l *loginLimiter) clear(key string) {
	l.mu.Lock()
	delete(l.attempts, key)
	l.mu.Unlock()
}

// sweep drops expired entries so the map does not grow without bound.
func (l *loginLimiter) sweep(now time.Time) {
	if len(l.attempts) < 1000 {
		return
	}
	for k, w := range l.attempts {
		if now.After(w.reset) {
			delete(l.attempts, k)
		}
	}
}

// --- middleware ---

// withSession resolves the session from the cookie and attaches the
// authorization context to the request. It does not reject requests without a
// session: requireAuth decides what gets rejected.
func (s *Server) withSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookie)
		if err != nil || cookie.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		user, err := s.identity.LookupSession(r.Context(), cookie.Value)
		if err != nil {
			// Clear an invalid or expired cookie, so the browser stops
			// carrying a dead token on every request.
			if errors.Is(err, identity.ErrNotFound) {
				s.clearSessionCookie(w, r)
			}
			next.ServeHTTP(w, r)
			return
		}
		access, err := s.identity.LoadAccess(r.Context(), user)
		if err != nil {
			s.log.Error("could not load authorization data", "user", user.ID, "err", err)
			next.ServeHTTP(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), ctxAccess, access)
		ctx = context.WithValue(ctx, ctxToken, cookie.Value)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAuth demands a session.
func requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if accessFrom(r) == nil {
			writeErrorCode(w, http.StatusUnauthorized, "you need to sign in", "unauthenticated")
			return
		}
		h(w, r)
	}
}

// requirePermission demands a specific permission alongside the session.
func requirePermission(p identity.Permission, h http.HandlerFunc) http.HandlerFunc {
	return requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if !accessFrom(r).Can(p) {
			writeErrorCode(w, http.StatusForbidden, "you do not have permission for this", "forbidden")
			return
		}
		h(w, r)
	})
}

// --- endpoints ---

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	key := clientIP(r) + "|" + strings.ToLower(strings.TrimSpace(req.Email))
	if !s.logins.allow(key) {
		writeErrorCode(w, http.StatusTooManyRequests,
			"too many failed attempts, try again in a few minutes", "rate_limited")
		return
	}

	user, err := s.identity.Authenticate(r.Context(), req.Email, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, identity.ErrBadCredential):
			writeErrorCode(w, http.StatusUnauthorized, "wrong email or password", "bad_credentials")
		case errors.Is(err, identity.ErrInactive):
			writeErrorCode(w, http.StatusForbidden, "your account is inactive", "inactive")
		default:
			s.log.Error("authentication error", "err", err)
			writeError(w, http.StatusInternalServerError, errors.New("could not sign in"))
		}
		return
	}
	s.logins.clear(key)

	token, expires, err := s.identity.CreateSession(r.Context(), user.ID, r.UserAgent(), SessionTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
	})

	access, err := s.identity.LoadAccess(r.Context(), user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.log.Info("signed in", "user", user.Email, "ip", clientIP(r))
	writeJSON(w, meResponse(access))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token, ok := r.Context().Value(ctxToken).(string); ok {
		if err := s.identity.DeleteSession(r.Context(), token); err != nil {
			s.log.Warn("could not delete the session", "err", err)
		}
	}
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	access := accessFrom(r)
	if access == nil {
		writeErrorCode(w, http.StatusUnauthorized, "you need to sign in", "unauthenticated")
		return
	}
	writeJSON(w, meResponse(access))
}

// handleChangeOwnPassword lets a user change their own password.
func (s *Server) handleChangeOwnPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"currentPassword"`
		New     string `json:"newPassword"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	access := accessFrom(r)
	if _, err := s.identity.Authenticate(r.Context(), access.User.Email, req.Current); err != nil {
		writeErrorCode(w, http.StatusUnauthorized, "the current password is wrong", "bad_credentials")
		return
	}
	if err := s.identity.SetPassword(r.Context(), access.User.ID, req.New); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// SetPassword closes every session; the user will sign in again.
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// meResponse gathers everything the UI needs into a single response: who the
// user is, what they can do, and which projects they reach.
func meResponse(a *identity.Access) map[string]any {
	perms := make([]identity.Permission, 0, len(a.Permissions))
	for p := range a.Permissions {
		perms = append(perms, p)
	}
	return map[string]any{
		"user":         a.User,
		"permissions":  perms,
		"isSuperAdmin": a.User.IsSuperAdmin,
		"projects":     a.Projects,
		"applications": a.Applications,
	}
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// isSecureRequest decides whether the cookie is marked Secure. Behind a
// reverse proxy that terminates TLS, r.TLS is nil, so X-Forwarded-Proto is
// taken into account as well.
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if i := strings.IndexByte(fwd, ','); i > 0 {
			return strings.TrimSpace(fwd[:i])
		}
		return strings.TrimSpace(fwd)
	}
	if i := strings.LastIndexByte(r.RemoteAddr, ':'); i > 0 {
		return r.RemoteAddr[:i]
	}
	return r.RemoteAddr
}
