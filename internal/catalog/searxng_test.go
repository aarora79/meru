// This file tests CheckSearXNG against httptest servers that answer JSON,
// answer HTML the way a SearXNG with JSON off does, and don't answer at
// all. The bodies copy what SearXNG 2026.9 sends.

package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckSearXNG(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr error // nil means the check passes
		wantMsg string
	}{
		{"json", 200, `{"query": "", "results": []}`, nil, ""},
		{"json refusing the empty query", 400, `{"error": "No query"}`, nil, ""},
		{"json off", 403, "<!doctype html>\n<title>403 Forbidden</title>", ErrSearXNGNoJSON, ""},
		{"html page", 200, "<html><body>results</body></html>", ErrSearXNGNoJSON, ""},
		{"server error", 500, "boom", nil, "answered 500"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var query string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				query = r.URL.RawQuery
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()
			err := CheckSearXNG(context.Background(), srv.URL+"/")
			switch {
			case tt.wantErr != nil && !errors.Is(err, tt.wantErr):
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			case tt.wantMsg != "" && (err == nil || !strings.Contains(err.Error(), tt.wantMsg)):
				t.Errorf("err = %v, want one holding %q", err, tt.wantMsg)
			case tt.wantErr == nil && tt.wantMsg == "" && err != nil:
				t.Errorf("err = %v, want nil", err)
			}
			if query != "q=&format=json" {
				t.Errorf("query = %q", query)
			}
		})
	}

	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	if err := CheckSearXNG(context.Background(), addr); !errors.Is(err, ErrSearXNGDown) {
		t.Errorf("closed port: err = %v, want ErrSearXNGDown", err)
	}
}
