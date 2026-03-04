package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"llm_gateway/auth/redis"
)

const tokenPrefix = "sk"
const tokenEntropyLen = 32

type Middleware func(http.Handler) http.Handler

// Chain composes middlewares and wraps the final handler.
// Middlewares are applied in the order they are passed (first = outermost).
func Chain(h http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

// CORSMiddleware handles browser CORS preflight (OPTIONS) requests and sets
// CORS headers on all responses.
func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")

		requestedHeaders := r.Header.Get("Access-Control-Request-Headers")
		if requestedHeaders != "" {
			w.Header().Set("Access-Control-Allow-Headers", requestedHeaders)
		} else {
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}

		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func authErrorJSON(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	body, _ := json.Marshal(map[string]interface{}{
		"error": map[string]string{
			"message": message,
			"type":    "invalid_api_key",
		},
	})
	w.Write(body)
}

func AuthCheckMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			authErrorJSON(w, "Missing Authorization header")
			return
		}

		// strip "Bearer " prefix
		token, found := strings.CutPrefix(authHeader, "Bearer ")
		if !found || token == "" {
			authErrorJSON(w, "Authorization header must be: Bearer token")
			return
		}

		if !redis.CheckTokenFormat(tokenPrefix, tokenEntropyLen, token) {
			authErrorJSON(w, "Invalid token format")
			return
		}

		// check if token in auth DB
		isValid, _, err := authService.Get(token)
		if err != nil {
			logError("AuthCheckMiddleware: auth service error: %s", err)
			authErrorJSON(w, "Authentication service unavailable")
			return
		}
		if !isValid {
			authErrorJSON(w, "Invalid or revoked token")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func AdminCheckMiddleware(next http.Handler) http.Handler {
	secret := os.Getenv("ADMIN_SECRET")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Admin-Secret") != secret || secret == "" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
