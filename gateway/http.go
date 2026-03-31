package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	"golang.org/x/time/rate"
)

const tokenPrefix = "sk"
const tokenEntropyLen = 32

const tokenGenSpeed = 100
const tokenCapacity = 200
const parallelCount = 50

type Middleware func(http.Handler) http.Handler

var (
	rateLimiter       = rate.NewLimiter(rate.Limit(tokenGenSpeed), tokenCapacity)
	parallelSemaphore = make(chan struct{}, parallelCount)
)

func chain(h http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

func adminCheckMiddleware(next http.Handler) http.Handler {
	secret := os.Getenv("ADMIN_SECRET")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Admin-Secret") != secret || secret == "" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bindJSON(r *http.Request, obj interface{}) error {
	if r.Body == nil {
		return errors.New("request body is empty")
	}
	defer r.Body.Close()

	decoder := json.NewDecoder(r.Body)
	err := decoder.Decode(obj)
	if err != nil {
		return fmt.Errorf("json decode error: %w", err)
	}

	return nil
}
