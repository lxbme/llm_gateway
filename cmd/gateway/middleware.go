package main

import (
	"net/http"
	"os"

	"golang.org/x/time/rate"
)

const tokenPrefix = "sk"
const tokenEntropyLen = 32

// rate limit arguments
const tokenGenSpeed = 100
const tokenCapacity = 200
const parallelCount = 50

type Middleware func(http.Handler) http.Handler

var (
	rateLimiter       = rate.NewLimiter(rate.Limit(tokenGenSpeed), tokenCapacity)
	parallelSemaphore = make(chan struct{}, parallelCount)
)

// Chain composes middlewares and wraps the final handler.
// Middlewares are applied in the order they are passed (first = outermost).
func Chain(h http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
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
