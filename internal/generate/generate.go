// Package generate is the "EmptySlot -> Generating -> Valid" step: it turns
// a requirement's Do's into a live PNR against the real target system
// (currently PSS at https://rag-chatbot-project-1.onrender.com, per the
// design doc's "Generation" gap).
package generate

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/soumya-ranjan-000/tdms/internal/model"
	"github.com/soumya-ranjan-000/tdms/internal/pss"
	"github.com/soumya-ranjan-000/tdms/internal/reldate"
)

// cabinToBookingClass maps the requirement's cabin to PSS's single-letter
// RBD booking class. Not part of the dictionary: the evaluator never checks
// cabin today, so a wrong mapping fails loudly at generation, not silently
// at validation.
var cabinToBookingClass = map[string]string{
	"economy":         "Y",
	"premium_economy": "W",
	"business":        "J",
	"first":           "F",
}

type Generator struct {
	PSS *pss.Client
	// Logf, if set, receives one line per generation step for the run log.
	Logf func(format string, args ...any)
}

func (g *Generator) logf(format string, args ...any) {
	if g.Logf != nil {
		g.Logf(format, args...)
	}
}

func New(pssClient *pss.Client) *Generator {
	return &Generator{PSS: pssClient}
}

// Generate materializes a live PNR matching req and returns its test data:
// the record locator and the lead passenger's last name, which a test needs
// to retrieve the booking. testCaseKey seeds deterministic, collision-free
// synthetic passenger identities so repeated regenerations of the same slot
// reuse the same PSS passenger profiles instead of accumulating new ones.
//
// onBooked, if non-nil, runs the moment PSS has created the booking — before
// payment and ticketing — so the caller can record it while a later step can
// still fail. Once a booking exists, the returned PNR is non-empty even when
// err is not nil.
func (g *Generator) Generate(ctx context.Context, req model.Requirement, testCaseKey string, onBooked func(model.TestDatum) error) (model.TestDatum, error) {
	var none model.TestDatum
	origin, destination, err := parseRoute(req.Route)
	if err != nil {
		return none, err
	}

	bookingClass, ok := cabinToBookingClass[strings.ToLower(req.Cabin)]
	if !ok {
		return none, fmt.Errorf("cabin %q has no known booking-class mapping", req.Cabin)
	}

	if err := rejectUnsupportedAncillaries(req.Ancillaries); err != nil {
		return none, err
	}

	passengers, err := buildPassengers(req.Passengers, testCaseKey)
	if err != nil {
		return none, err
	}

	now := time.Now()
	depart, err := reldate.Parse(req.Depart, now)
	if err != nil {
		return none, fmt.Errorf("requirement.depart: %w", err)
	}

	bookingReq := pss.CreateBookingRequest{
		PassengerID:  "tdms-generated",
		Origin:       origin,
		Destination:  destination,
		Date:         depart.Format("2006-01-02"),
		Status:       "confirmed",
		BookingClass: bookingClass,
		Passengers:   passengers,
	}

	if req.TripType == "round_trip" {
		if req.Return == "" {
			return none, fmt.Errorf("trip_type is round_trip but requirement.return is not set")
		}
		ret, err := reldate.Parse(req.Return, now)
		if err != nil {
			return none, fmt.Errorf("requirement.return: %w", err)
		}
		bookingReq.ReturnDate = ret.Format("2006-01-02")
		bookingReq.ReturnBookingClass = bookingClass
	}

	booking, err := g.PSS.CreateBooking(ctx, bookingReq)
	if err != nil {
		return none, fmt.Errorf("creating booking: %w", err)
	}
	// PSS files the booking under the names TDMS sent, so the lead
	// passenger's last name is known without another lookup.
	booked := model.TestDatum{PNR: booking.PNR, LastName: passengers[0].LastName}
	route := origin + "→" + destination + " " + bookingReq.Date
	if bookingReq.ReturnDate != "" {
		route += " / return " + bookingReq.ReturnDate
	}
	g.logf("Booking created on PSS: PNR %s (%s, class %s, %d passenger(s), lead last name %s)",
		booking.PNR, route, bookingClass, len(passengers), booked.LastName)
	if onBooked != nil {
		if err := onBooked(booked); err != nil {
			return booked, fmt.Errorf("recording booked PNR %s: %w", booking.PNR, err)
		}
	}

	if req.Ticketed {
		g.logf("Capturing payment of $%.2f for %s", booking.Price, booking.PNR)
		if err := g.PSS.ProcessPayment(ctx, booking.PNR, booking.Price); err != nil {
			return booked, fmt.Errorf("paying for booking %s: %w", booking.PNR, err)
		}
		g.logf("Payment captured; issuing ticket(s) for %s", booking.PNR)
		if err := g.PSS.IssueTicket(ctx, booking.PNR); err != nil {
			return booked, fmt.Errorf("issuing ticket for booking %s: %w", booking.PNR, err)
		}
	}

	if req.Ticketed {
		g.logf("Ticket(s) issued for %s", booking.PNR)
	}
	return booked, nil
}

func parseRoute(route string) (origin, destination string, err error) {
	parts := strings.Split(route, "-")
	if len(parts) != 2 || len(parts[0]) != 3 || len(parts[1]) != 3 {
		return "", "", fmt.Errorf("route %q doesn't match the ORG-DST grammar (e.g. AUH-LHR)", route)
	}
	return parts[0], parts[1], nil
}

// rejectUnsupportedAncillaries fails loudly on anything but "none": ancillary
// generation isn't designed yet, so silently dropping a requested seat/bag/
// lounge would hand back a PNR that doesn't match the Do's.
func rejectUnsupportedAncillaries(ancillaries map[string]string) error {
	for name, value := range ancillaries {
		if value != "" && !strings.EqualFold(value, "none") {
			return fmt.Errorf("ancillary generation not implemented yet (requirement declares %s: %s)", name, value)
		}
	}
	return nil
}

// buildPassengers turns the requirement's {type: count} map into PSS
// passenger records with deterministic, synthetic identities.
func buildPassengers(counts map[string]int, testCaseKey string) ([]pss.PassengerInput, error) {
	slug := slugify(testCaseKey)
	types := make([]string, 0, len(counts))
	for pType := range counts {
		types = append(types, pType)
	}
	sort.Strings(types)

	var passengers []pss.PassengerInput
	for _, pType := range types {
		count := counts[pType]
		if count <= 0 {
			continue
		}
		for i := 0; i < count; i++ {
			suffix := string(rune('A' + i))
			passengers = append(passengers, pss.PassengerInput{
				FirstName:     "Tdms",
				LastName:      capitalize(strings.ToLower(pType)) + suffix,
				Email:         fmt.Sprintf("tdms.%s.%s%s@tdms.local", slug, strings.ToLower(pType), suffix),
				PassengerType: pType,
			})
		}
	}
	if len(passengers) == 0 {
		return nil, fmt.Errorf("requirement.passengers has no passengers with count > 0")
	}
	return passengers, nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}
