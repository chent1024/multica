package localworkbench

import (
	"net"
	"net/http"
)

func LoopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "loopback client required", http.StatusForbidden)
			return
		}
		address := net.ParseIP(host)
		if address == nil || !address.IsLoopback() {
			http.Error(w, "loopback client required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
