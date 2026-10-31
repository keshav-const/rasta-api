// Package httpx holds the shared HTTP response contract: one JSON envelope and
// one error shape for the entire API, so every endpoint looks consistent.
package httpx

import (
	"encoding/json"
	"net/http"
)

// Envelope is the single response shape for the whole API.
//
//	{ "data": ..., "meta": ... }      on success
//	{ "error": { code, message } }    on failure
type Envelope struct {
	Data  any       `json:"data,omitempty"`
	Meta  any       `json:"meta,omitempty"`
	Error *APIError `json:"error,omitempty"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// JSON writes a success envelope.
func JSON(w http.ResponseWriter, status int, data, meta any) {
	write(w, status, Envelope{Data: data, Meta: meta})
}

// Error writes an error envelope with a stable machine-readable code.
func Error(w http.ResponseWriter, status int, code, message string) {
	write(w, status, Envelope{Error: &APIError{Code: code, Message: message}})
}

func write(w http.ResponseWriter, status int, body Envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
