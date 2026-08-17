// Package httpapi contains transport concerns shared by every control-plane
// feature. Domain packages must not depend on it.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

type requestIDKey struct{}

// RequestID returns the server-generated request identifier, if the request
// passed through AccessLog.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// AccessLog adds a request id and emits one structured completion event. Read
// polling is DEBUG; mutations are INFO; client and server failures are WARN and
// ERROR respectively.
func AccessLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := newRequestID()
		w.Header().Set("X-Request-ID", requestID)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, requestID))

		recorder := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)

		level := slog.LevelDebug
		switch {
		case recorder.status >= http.StatusInternalServerError:
			level = slog.LevelError
		case recorder.status >= http.StatusBadRequest:
			level = slog.LevelWarn
		case r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions:
			level = slog.LevelInfo
		}
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		logger.LogAttrs(r.Context(), level, "http.request",
			slog.String("request_id", requestID),
			slog.String("method", r.Method),
			slog.String("route", route),
			slog.Int("status", recorder.status),
			slog.Int("bytes", recorder.bytes),
			slog.Int64("duration_ms", time.Since(started).Milliseconds()),
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.status = status
	w.wrote = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += n
	return n, err
}

// Unwrap lets net/http.ResponseController reach optional interfaces on the
// underlying writer without this middleware reimplementing all of them.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}
