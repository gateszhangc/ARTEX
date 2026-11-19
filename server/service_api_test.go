package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServiceAuthRequiresTokenAndValidOwner(t *testing.T) {
	s := &Server{serviceToken: "service-token-for-tests"}
	called := 0
	handler := s.requireServiceAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if serviceOwner(r) != "user-123" {
			t.Fatalf("owner = %q", serviceOwner(r))
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	cases := []struct {
		name   string
		token  string
		owner  string
		status int
	}{
		{"missing token", "", "user-123", http.StatusUnauthorized},
		{"wrong token", "wrong", "user-123", http.StatusUnauthorized},
		{"missing owner", "service-token-for-tests", "", http.StatusBadRequest},
		{"invalid owner", "service-token-for-tests", "../../other", http.StatusBadRequest},
		{"valid", "service-token-for-tests", "user-123", http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := called
			request := httptest.NewRequest(http.MethodGet, "/api/service/health", nil)
			if tc.token != "" {
				request.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.owner != "" {
				request.Header.Set(serviceOwnerHeader, tc.owner)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, tc.status, response.Body.String())
			}
			if tc.status == http.StatusNoContent && called != before+1 {
				t.Fatal("valid request did not reach the service handler")
			}
			if tc.status != http.StatusNoContent && called != before {
				t.Fatal("rejected request reached the service handler")
			}
		})
	}
}

func TestServiceAuthDisabledReturnsNotFound(t *testing.T) {
	s := &Server{}
	handler := s.requireServiceAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("disabled service handler must not run")
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/service/health", nil)
	request.Header.Set("Authorization", "Bearer anything")
	request.Header.Set(serviceOwnerHeader, "user-123")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}
