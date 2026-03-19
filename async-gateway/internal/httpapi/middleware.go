package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
)

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

func extractModelFromGenerateContentPath(path string) string {
	const prefix = "/v1beta/models/"
	const suffix = ":generateContent"

	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}

	model := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if strings.Contains(model, "/") {
		return ""
	}
	return model
}
