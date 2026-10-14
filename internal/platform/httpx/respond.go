// Package httpx contains the HTTP plumbing shared by the kernel and modules:
// a small router on top of net/http.ServeMux, middleware, and the single
// place where errors are translated into HTTP responses.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/mini-base/mini-base/internal/platform/errs"
)

// ErrorBody is the JSON envelope returned for every error.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the content of ErrorBody.
type ErrorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	// Encoding errors after WriteHeader cannot be reported to the client.
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError translates err into the standard error envelope. Internal errors
// are logged with their cause and returned to the client as a generic message.
func WriteError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	e, ok := errs.As(err)
	if !ok {
		e = errs.Internal("internal", err)
	}

	status := StatusFor(e.Kind)
	if status >= http.StatusInternalServerError && log != nil {
		log.ErrorContext(r.Context(), "request failed",
			slog.String("code", e.Code),
			slog.String("request_id", RequestID(r.Context())),
			slog.Any("error", err),
		)
	}

	WriteJSON(w, status, ErrorBody{Error: ErrorDetail{
		Code:      e.Code,
		Message:   e.Message,
		RequestID: RequestID(r.Context()),
	}})
}

// StatusFor maps an error kind to its HTTP status code.
func StatusFor(k errs.Kind) int {
	switch k {
	case errs.KindInvalid:
		return http.StatusBadRequest
	case errs.KindUnauthorized:
		return http.StatusUnauthorized
	case errs.KindForbidden:
		return http.StatusForbidden
	case errs.KindNotFound:
		return http.StatusNotFound
	case errs.KindConflict:
		return http.StatusConflict
	case errs.KindTooLarge:
		return http.StatusRequestEntityTooLarge
	case errs.KindRateLimited:
		return http.StatusTooManyRequests
	case errs.KindUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// Errors returned by DecodeJSON.
var (
	ErrUnsupportedMediaType = errs.New(errs.KindInvalid, "request.unsupported_media_type", "Content-Type must be application/json")
	ErrMalformedJSON        = errs.Invalid("request.malformed_json", "Request body is not valid JSON")
	ErrBodyTooLarge         = errs.New(errs.KindTooLarge, "request.body_too_large", "Request body is too large")
)

// DecodeJSON decodes a single JSON object from the request body into dst,
// rejecting unknown fields and trailing data. Body size limits are enforced
// by the MaxBodySize middleware.
func DecodeJSON(r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		return ErrUnsupportedMediaType
	}

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return ErrBodyTooLarge.Wrap(err)
		}
		return ErrMalformedJSON.Wrap(err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrMalformedJSON.Wrap(errors.New("body must contain a single JSON object"))
	}
	return nil
}
