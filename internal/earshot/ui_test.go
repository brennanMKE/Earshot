package earshot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUploadUI(t *testing.T) {
	for _, noAuth := range []bool{true, false} {
		handler := NewHandler(Config{NoAuth: noAuth}, nil)
		page := httptest.NewRecorder()
		handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/ui/", nil))
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `type="file"`) ||
			!strings.Contains(page.Body.String(), `id="record"`) ||
			strings.Contains(page.Body.String(), `id="token"`) == noAuth ||
			page.Header().Get("Permissions-Policy") != "microphone=(self)" ||
			page.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unexpected UI response in no-auth mode %t: %d %s", noAuth, page.Code, page.Body)
		}
		for _, asset := range []string{"app.js", "style.css"} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ui/"+asset, nil))
			if response.Code != http.StatusOK || response.Body.Len() == 0 {
				t.Fatalf("missing UI asset %s: %d", asset, response.Code)
			}
		}
		redirect := httptest.NewRecorder()
		handler.ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "/ui", nil))
		if redirect.Code != http.StatusPermanentRedirect || redirect.Header().Get("Location") != "ui/" {
			t.Fatalf("unexpected UI redirect: %d %s", redirect.Code, redirect.Header().Get("Location"))
		}
	}
}
