package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
)

// ProblemError is one entry of the `errors` list of a validation_failed problem.
type ProblemError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// WriteProblem writes an application/problem+json response (RFC 9457) as described by the
// Problem schema of the contract. The title is a fixed, safe text; callers never put request data
// or internal error text into it.
func WriteProblem(w http.ResponseWriter, status int, code ErrorCode, fieldErrors ...ProblemError) {
	p := struct {
		Type   string         `json:"type"`
		Title  string         `json:"title"`
		Status int            `json:"status"`
		Code   ErrorCode      `json:"code"`
		Errors []ProblemError `json:"errors,omitempty"`
	}{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Code:   code,
		Errors: fieldErrors,
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	WriteProblem(w, http.StatusNotImplemented, ErrorCodeInternalError)
}

// requestErrorHandler turns the errors of the generated parameter parsing into problems. The
// error text is never echoed because it can contain the offending request data.
func requestErrorHandler(w http.ResponseWriter, _ *http.Request, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		WriteProblem(w, http.StatusRequestEntityTooLarge, ErrorCodePayloadTooLarge)
		return
	}
	if field, ok := paramName(err); ok {
		WriteProblem(w, http.StatusBadRequest, ErrorCodeValidationFailed, ProblemError{Field: field, Message: "invalid or missing value"})
		return
	}
	WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest)
}

func paramName(err error) (string, bool) {
	var (
		invalid     *InvalidParamFormatError
		required    *RequiredParamError
		header      *RequiredHeaderError
		tooMany     *TooManyValuesForParamError
		unmarshal   *UnmarshalingParamError
		cookieError *UnescapedCookieParamError
	)
	switch {
	case errors.As(err, &invalid):
		return invalid.ParamName, true
	case errors.As(err, &required):
		return required.ParamName, true
	case errors.As(err, &header):
		return header.ParamName, true
	case errors.As(err, &tooMany):
		return tooMany.ParamName, true
	case errors.As(err, &unmarshal):
		return unmarshal.ParamName, true
	case errors.As(err, &cookieError):
		return cookieError.ParamName, true
	}
	return "", false
}

// problemFallback answers unknown routes and wrong methods with problems instead of the plain text
// of http.ServeMux.
type problemFallback struct{ mux routeMux }

func (f problemFallback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h, pattern := f.mux.Handler(r)
	if pattern != "" {
		f.mux.ServeHTTP(w, r)
		return
	}
	h.ServeHTTP(&fallbackWriter{ResponseWriter: w}, r)
}

// fallbackWriter replaces the 404 and 405 texts of the mux with problems and passes every other
// response (redirects) through. The Allow header of a 405 is kept.
type fallbackWriter struct {
	http.ResponseWriter
	replaced bool
}

func (f *fallbackWriter) WriteHeader(status int) {
	switch status {
	case http.StatusNotFound:
		f.replaced = true
		WriteProblem(f.ResponseWriter, status, ErrorCodeNotFound)
	case http.StatusMethodNotAllowed:
		f.replaced = true
		WriteProblem(f.ResponseWriter, status, ErrorCodeInvalidRequest)
	default:
		f.ResponseWriter.WriteHeader(status)
	}
}

func (f *fallbackWriter) Write(b []byte) (int, error) {
	if f.replaced {
		return len(b), nil
	}
	return f.ResponseWriter.Write(b)
}

func (f *fallbackWriter) Unwrap() http.ResponseWriter { return f.ResponseWriter }
