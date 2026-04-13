package auth

import "net/http"

func Middleware(authenticator *Authenticator, next http.Handler) http.Handler {
	if authenticator == nil || !authenticator.Enabled() {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := authenticator.AuthenticateRequest(r)
		if err != nil {
			Challenge(w)
			return
		}

		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
	})
}
