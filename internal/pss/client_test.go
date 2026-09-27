package pss

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetBookingDistinguishesNotFoundFromOutage(t *testing.T) {
	cases := map[int]bool{http.StatusNotFound: true, http.StatusInternalServerError: false, http.StatusBadGateway: false}
	for status, wantNotFound := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		_, err := New(srv.URL).GetBooking(context.Background(), "ABC123")
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: expected an error", status)
		}
		if got := errors.Is(err, ErrNotFound); got != wantNotFound {
			t.Errorf("status %d: errors.Is(ErrNotFound)=%v, want %v", status, got, wantNotFound)
		}
	}
}
