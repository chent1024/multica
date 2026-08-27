package localworkbench

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoopbackOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		remoteAddr string
		wantStatus int
	}{
		{name: "IPv4 loopback", remoteAddr: "127.0.0.1:1234", wantStatus: http.StatusNoContent},
		{name: "IPv6 loopback", remoteAddr: "[::1]:1234", wantStatus: http.StatusNoContent},
		{name: "non-loopback", remoteAddr: "192.0.2.10:1234", wantStatus: http.StatusForbidden},
		{name: "malformed", remoteAddr: "127.0.0.1", wantStatus: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/health", nil)
			request.RemoteAddr = test.remoteAddr
			handler := LoopbackOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
		})
	}
}
