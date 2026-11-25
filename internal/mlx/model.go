package mlx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	DefaultModel = "large-v3-turbo"
	modelSource  = "https://huggingface.co/mlx-community/whisper-large-v3-turbo/resolve/a4aaeec0636e6fef84abdcbe3544cb2bf7e9f6fb/"
)

type modelFile struct {
	name string
	size int64
	hash string
}

var turboFiles = []modelFile{
	{"config.json", 268, "b34fc29e4e11e0a25e812775dd67f4dd16fc2c8eb43d28ae25ff7d660ecb6379"},
	{"weights.safetensors", 1613977612, "951ed3fc1203e6a62467abb2144a96ce7eafca8fa77e3704fdb8635ff3e7f8a6"},
}

// Fetch ensures the pinned model is present and returns its local directory.
func Fetch(ctx context.Context, root, model string) (string, error) {
	if model != DefaultModel {
		return "", fmt.Errorf("unsupported model %q (available: %s)", model, DefaultModel)
	}
	dir, err := filepath.Abs(filepath.Join(root, model))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 20 * time.Minute}
	for _, asset := range turboFiles {
		if err := ensureFile(ctx, client, modelSource, dir, asset); err != nil {
			return "", fmt.Errorf("%s: %w", asset.name, err)
		}
	}
	return dir, nil
}

func ensureFile(ctx context.Context, client *http.Client, source, dir string, asset modelFile) error {
	path := filepath.Join(dir, asset.name)
	existing, err := os.Open(path)
	if err == nil {
		verifyErr := verifyFile(io.Discard, existing, asset)
		existing.Close()
		if verifyErr == nil {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	// A fresh query avoids cached redirects to expired signed download URLs.
	url := source + asset.name + "?download=true&earshot=" + strconv.FormatInt(time.Now().UnixNano(), 10)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("model server returned HTTP %d", response.StatusCode)
	}
	file, err := os.CreateTemp(dir, "."+asset.name+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	verifyErr := verifyFile(file, response.Body, asset)
	closeErr := file.Close()
	if verifyErr != nil {
		return verifyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

func verifyFile(dst io.Writer, src io.Reader, asset modelFile) error {
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(dst, hash), io.LimitReader(src, asset.size+1))
	if err != nil {
		return err
	}
	if size != asset.size || hex.EncodeToString(hash.Sum(nil)) != asset.hash {
		return errors.New("model file does not match the pinned size and SHA-256")
	}
	return nil
}
