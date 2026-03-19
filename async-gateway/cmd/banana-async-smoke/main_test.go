package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPromptNonEmpty(t *testing.T) {
	if defaultPrompt == "" {
		t.Fatalf("defaultPrompt must not be empty")
	}
}

func TestLoadRequestBodyFromFile(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	bodyFile := filepath.Join(tempDir, "request.json")
	body := []byte(`{"contents":[{"parts":[{"text":"叉烧包"}]}]}`)
	if err := os.WriteFile(bodyFile, body, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := loadRequestBodyFromFile(bodyFile)
	if err != nil {
		t.Fatalf("loadRequestBodyFromFile() error = %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("loadRequestBodyFromFile() = %s, want %s", string(got), string(body))
	}
}
