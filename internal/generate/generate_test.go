package generate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/soumya-ranjan-000/tdms/internal/model"
	"github.com/soumya-ranjan-000/tdms/internal/pss"
)

// fakePSS records the order of calls and can fail a chosen path.
type fakePSS struct {
	mu       sync.Mutex
	calls    []string
	failPath string
	booked   map[string]any
}

func (f *fakePSS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.URL.Path)
	if f.failPath != "" && strings.HasSuffix(r.URL.Path, f.failPath) {
		http.Error(w, "boom", http.StatusBadRequest)
		return
	}
	if r.URL.Path == "/api/pss/bookings" {
		json.NewDecoder(r.Body).Decode(&f.booked)
		json.NewEncoder(w).Encode(map[string]any{"pnr": "ABC123", "price": 710.5})
		return
	}
	w.Write([]byte(`{}`))
}

func roundTrip() model.Requirement {
	return model.Requirement{
		TripType:    "round_trip",
		Passengers:  map[string]int{"ADT": 1},
		Route:       "AUH-LHR",
		Depart:      "T+90d",
		Return:      "T+97d",
		Cabin:       "economy",
		Ticketed:    true,
		Ancillaries: map[string]string{"seat": "none"},
	}
}

func TestGenerateRecordsPNRBeforePayingAndTicketing(t *testing.T) {
	fake := &fakePSS{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	var callsAtBooking int
	var recorded model.TestDatum
	got, err := New(pss.New(srv.URL)).Generate(context.Background(), roundTrip(), "ACP-TC-1", func(d model.TestDatum) error {
		callsAtBooking = len(fake.calls)
		recorded = d
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := model.TestDatum{PNR: "ABC123", LastName: "AdtA"}
	if got != want || recorded != want {
		t.Fatalf("got %+v, recorded %+v, want %+v", got, recorded, want)
	}
	wantCalls := []string{"/api/pss/bookings", "/api/pss/bookings/ABC123/payment", "/api/pss/bookings/ABC123/ticket"}
	if strings.Join(fake.calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("calls = %v, want %v", fake.calls, wantCalls)
	}
	if callsAtBooking != 1 {
		t.Fatalf("onBooked ran after %d calls, want 1 (before payment)", callsAtBooking)
	}
	if fake.booked["return_date"] == nil || fake.booked["booking_class"] != "Y" {
		t.Fatalf("unexpected booking payload: %v", fake.booked)
	}
}

func TestGenerateReturnsPNRWhenLaterStepFails(t *testing.T) {
	fake := &fakePSS{failPath: "/ticket"}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	got, err := New(pss.New(srv.URL)).Generate(context.Background(), roundTrip(), "ACP-TC-1", nil)
	if err == nil {
		t.Fatal("expected the ticketing failure to surface")
	}
	if got.PNR != "ABC123" {
		t.Fatalf("got pnr %q, want the booked PNR so the caller can keep its trail", got.PNR)
	}
}

func TestGenerateRejectsBadRequirementsBeforeCallingPSS(t *testing.T) {
	cases := map[string]func(*model.Requirement){
		"round trip without return": func(r *model.Requirement) { r.Return = "" },
		"unsupported ancillary":     func(r *model.Requirement) { r.Ancillaries["seat"] = "window" },
		"unknown cabin":             func(r *model.Requirement) { r.Cabin = "steerage" },
		"bad route":                 func(r *model.Requirement) { r.Route = "AUHLHR" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			fake := &fakePSS{}
			srv := httptest.NewServer(fake)
			defer srv.Close()
			req := roundTrip()
			mutate(&req)
			if _, err := New(pss.New(srv.URL)).Generate(context.Background(), req, "ACP-TC-1", nil); err == nil {
				t.Fatal("expected an error")
			}
			if len(fake.calls) != 0 {
				t.Fatalf("PSS was called %d times for an invalid requirement", len(fake.calls))
			}
		})
	}
}

func TestBuildPassengersIsDeterministic(t *testing.T) {
	counts := map[string]int{"CHD": 1, "ADT": 2}
	first, err := buildPassengers(counts, "ACP-TC-1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, _ := buildPassengers(counts, "ACP-TC-1")
		for j := range first {
			if first[j] != again[j] {
				t.Fatalf("passenger order changed between calls: %v vs %v", first, again)
			}
		}
	}
	if first[0].Email != "tdms.acp-tc-1.adtA@tdms.local" || first[2].PassengerType != "CHD" {
		t.Fatalf("unexpected passengers: %+v", first)
	}
}
