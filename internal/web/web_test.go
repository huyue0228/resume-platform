package web

import (
	"net/http/httptest"
	"testing"
)

func TestPrivateAndRemovedEndpointsNeverUseSPAFallback(t *testing.T) {
	for _, path := range []string{"/media", "/media/resumes/private.pdf", "/media/resumes/private", "/admin/", "/admin", "/api/missing/"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		Handler().ServeHTTP(w, r)
		if w.Code != 404 {
			t.Errorf("%s status=%d", path, w.Code)
		}
	}
}
