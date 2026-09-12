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

// SessionCookie, oturum çerezinin adı.
const SessionCookie = "nabiz_session"

// SessionTTL, oturum ömrü.
const SessionTTL = 12 * time.Hour

type ctxKey int

const (
	ctxAccess ctxKey = iota
	ctxToken
)

// accessFrom, istek bağlamındaki yetkilendirme bilgisini verir.
func accessFrom(r *http.Request) *identity.Access {
	a, _ := r.Context().Value(ctxAccess).(*identity.Access)
	return a
}

// --- giriş deneme sınırlayıcı ---

// loginLimiter, aynı kaynaktan gelen parola denemelerini sınırlar.
//
// Kalıcı bir depo değil, kasıtlı: amaç kaba kuvvet denemesini pahalı kılmak,
// kusursuz bir savunma kurmak değil. Süreç yeniden başladığında sayaç
// sıfırlanır ve bu kabul edilebilir.
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

// allow, denemeye izin verilip verilmediğini söyler ve sayacı artırır.
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

// reset, başarılı girişten sonra sayacı temizler.
func (l *loginLimiter) clear(key string) {
	l.mu.Lock()
	delete(l.attempts, key)
	l.mu.Unlock()
}

// sweep, süresi dolmuş kayıtları atar; map süresiz büyümesin.
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

// --- ara katman ---

// withSession, çerezden oturumu çözer ve yetkilendirme bağlamını isteğe
// iliştirir. Oturum yoksa isteği reddetmez: kimin neyi reddedeceğine
// requireAuth karar verir.
func (s *Server) withSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookie)
		if err != nil || cookie.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		user, err := s.identity.LookupSession(r.Context(), cookie.Value)
		if err != nil {
			// Geçersiz ya da süresi geçmiş çerezi temizle: tarayıcı her
			// istekte ölü bir jeton taşımasın.
			if errors.Is(err, identity.ErrNotFound) {
				s.clearSessionCookie(w, r)
			}
			next.ServeHTTP(w, r)
			return
		}
		access, err := s.identity.LoadAccess(r.Context(), user)
		if err != nil {
			s.log.Error("yetki bilgisi yüklenemedi", "user", user.ID, "err", err)
			next.ServeHTTP(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), ctxAccess, access)
		ctx = context.WithValue(ctx, ctxToken, cookie.Value)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAuth, oturum şartı koyar.
func requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if accessFrom(r) == nil {
			writeErrorCode(w, http.StatusUnauthorized, "oturum açmanız gerekiyor", "unauthenticated")
			return
		}
		h(w, r)
	}
}

// requirePermission, oturumun yanında belirli bir yetki de ister.
func requirePermission(p identity.Permission, h http.HandlerFunc) http.HandlerFunc {
	return requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if !accessFrom(r).Can(p) {
			writeErrorCode(w, http.StatusForbidden, "bu işlem için yetkiniz yok", "forbidden")
			return
		}
		h(w, r)
	})
}

// --- uçlar ---

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
			"çok fazla başarısız deneme, birkaç dakika sonra tekrar deneyin", "rate_limited")
		return
	}

	user, err := s.identity.Authenticate(r.Context(), req.Email, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, identity.ErrBadCredential):
			writeErrorCode(w, http.StatusUnauthorized, "e-posta ya da parola hatalı", "bad_credentials")
		case errors.Is(err, identity.ErrInactive):
			writeErrorCode(w, http.StatusForbidden, "hesabınız pasif durumda", "inactive")
		default:
			s.log.Error("kimlik doğrulama hatası", "err", err)
			writeError(w, http.StatusInternalServerError, errors.New("giriş yapılamadı"))
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
	s.log.Info("giriş yapıldı", "user", user.Email, "ip", clientIP(r))
	writeJSON(w, meResponse(access))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token, ok := r.Context().Value(ctxToken).(string); ok {
		if err := s.identity.DeleteSession(r.Context(), token); err != nil {
			s.log.Warn("oturum silinemedi", "err", err)
		}
	}
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	access := accessFrom(r)
	if access == nil {
		writeErrorCode(w, http.StatusUnauthorized, "oturum açmanız gerekiyor", "unauthenticated")
		return
	}
	writeJSON(w, meResponse(access))
}

// handleChangeOwnPassword, kullanıcının kendi parolasını değiştirmesi.
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
		writeErrorCode(w, http.StatusUnauthorized, "mevcut parola hatalı", "bad_credentials")
		return
	}
	if err := s.identity.SetPassword(r.Context(), access.User.ID, req.New); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// SetPassword tüm oturumları kapatır; kullanıcı yeniden giriş yapacak.
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// meResponse, arayüzün ihtiyaç duyduğu her şeyi tek yanıtta toplar:
// kim olduğu, ne yapabildiği, hangi projelere eriştiği.
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

// isSecureRequest, çerezin Secure işaretlenip işaretlenmeyeceğini belirler.
// TLS'i sonlandıran bir ters vekil arkasında r.TLS boştur; bu yüzden
// X-Forwarded-Proto da dikkate alınır.
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
