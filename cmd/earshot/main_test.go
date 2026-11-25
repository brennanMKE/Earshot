package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestReadTokens(t *testing.T) {
	hash := sha256.Sum256([]byte("test token"))
	path := filepath.Join(t.TempDir(), "tokens.txt")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("# comment\n\n%x client\n", hash)), 0600); err != nil {
		t.Fatal(err)
	}
	tokens, err := readTokens(path)
	if err != nil || len(tokens) != 1 || tokens[0] != hash {
		t.Fatalf("could not read labeled token: %v", err)
	}
	for _, invalid := range []string{"", "raw-token label", fmt.Sprintf("%x", hash)} {
		if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readTokens(path); err == nil {
			t.Fatal("accepted malformed or empty token file")
		}
	}
}
