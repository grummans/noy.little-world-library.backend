package httpapi

import (
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// authLimiter is shared by register and login: both are public, both cost a bcrypt
// hash, and both are worth brute-forcing.
type authLimiter struct {
	once    sync.Once
	limiter *limiter
}

func (a *authLimiter) guard(next http.HandlerFunc) http.HandlerFunc {
	a.once.Do(func() { a.limiter = newLimiter(time.Minute, 10) })

	return func(w http.ResponseWriter, r *http.Request) {
		if !a.limiter.allow(clientIP(r), time.Now()) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts. Try again in a minute.")
			return
		}
		next(w, r)
	}
}

// clientIP prefers the proxy header because the API runs behind Nginx Proxy Manager.
// The API must not be exposed directly while this is trusted.
func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		for i := 0; i < len(forwarded); i++ {
			if forwarded[i] == ',' {
				return trimmedHost(forwarded[:i])
			}
		}
		return trimmedHost(forwarded)
	}
	return trimmedHost(r.RemoteAddr)
}

func trimmedHost(value string) string {
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	return value
}

// checkOrigin rejects cross-site state-changing requests. SameSite=Lax already stops
// the cookie from riding along, but a cross-site POST could still log a victim into
// an attacker's account, so mutating requests are checked explicitly.
func (s *Server) limit(next http.HandlerFunc) http.HandlerFunc {
	return s.limiter.guard(next)
}

func (s *Server) checkOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isMutating(r.Method) {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host {
					writeError(w, http.StatusForbidden, "bad_origin", "Request came from an unexpected origin.")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isMutating(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}
