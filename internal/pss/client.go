// Package pss is TDMS's adapter to the airline system it checks live PNRs
// against — here, the RAG-Chatbot-Project's PSS (Passenger Service
// System) mock backend. This is the "live" side of the design doc's
// health check: "TDMS looks instead: it reads the PNR's real state and
// decides."
package pss

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// ErrNotFound means PSS answered that the PNR does not exist — the airline
// killed or purged it. It is the only lookup failure that proves a PNR is
// dead; every other error means "couldn't check" and must never retire one.
var ErrNotFound = errors.New("pnr not found in PSS")

type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// New builds a client with a timeout long enough to ride out a Render
// free-tier cold start, which regularly exceeds 20s.
func New(baseURL string) *Client {
	return &Client{
		BaseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: 90 * time.Second},
	}
}

// Segment is one flight leg of a booking.
type Segment struct {
	FlightNumber      string `json:"flight_number"`
	Origin            string `json:"origin"`
	Destination       string `json:"destination"`
	DepartureDatetime string `json:"departure_datetime"`
	Status            string `json:"status"`
}

// BookingPassenger is one traveller on a booking.
type BookingPassenger struct {
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	IsPrimary bool   `json:"is_primary"`
}

// Booking is PSS's response shape for GET /api/pss/bookings/{pnr},
// trimmed to the fields TDMS reads: what the dictionary references, plus
// the passengers a test needs to retrieve the booking.
type Booking struct {
	PNR        string             `json:"pnr"`
	Status     string             `json:"status"`
	Segments   []Segment          `json:"segments"`
	Passengers []BookingPassenger `json:"passengers"`
}

// LeadLastName is the primary passenger's last name (the first passenger's
// when none is marked primary), or "" when the booking lists none.
func (b *Booking) LeadLastName() string {
	for _, p := range b.Passengers {
		if p.IsPrimary {
			return p.LastName
		}
	}
	if len(b.Passengers) > 0 {
		return b.Passengers[0].LastName
	}
	return ""
}

type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("pss API error %d: %s", e.status, e.body)
}

func (c *Client) do(ctx context.Context, method, path string, reqBody, out any) error {
	var body io.Reader
	if reqBody != nil {
		payload, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return &apiError{status: resp.StatusCode, body: string(respBody)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

// GetBooking fetches the live state of one PNR. A 404 is reported as
// ErrNotFound so callers can tell a dead PNR from an unreachable PSS.
func (c *Client) GetBooking(ctx context.Context, pnr string) (*Booking, error) {
	var booking Booking
	err := c.do(ctx, http.MethodGet, "/api/pss/bookings/"+url.PathEscape(pnr), nil, &booking)
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.status == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, pnr)
	}
	if err != nil {
		return nil, err
	}
	return &booking, nil
}

// PassengerInput is one traveller on a new booking, as PSS's
// POST /api/pss/bookings expects it.
type PassengerInput struct {
	FirstName     string `json:"first_name"`
	LastName      string `json:"last_name"`
	Email         string `json:"email"`
	PassengerType string `json:"passenger_type,omitempty"`
}

// CreateBookingRequest is the "generate" side of TDMS: what it takes to
// materialize a PNR that matches a requirement's Do's.
type CreateBookingRequest struct {
	PassengerID        string           `json:"passenger_id"`
	Origin             string           `json:"origin"`
	Destination        string           `json:"destination"`
	Date               string           `json:"date"`
	Status             string           `json:"status"`
	BookingClass       string           `json:"booking_class"`
	Passengers         []PassengerInput `json:"passengers,omitempty"`
	ReturnDate         string           `json:"return_date,omitempty"`
	ReturnBookingClass string           `json:"return_booking_class,omitempty"`
}

// CreateBookingResponse is the slice of PSS's booking-creation response
// TDMS needs: the PNR to remember, and the price to charge if the
// requirement calls for ticketing.
type CreateBookingResponse struct {
	PNR   string  `json:"pnr"`
	Price float64 `json:"price"`
}

// CreateBooking is the "EmptySlot -> Generating" step: it materializes a
// brand-new PNR against the live PSS.
func (c *Client) CreateBooking(ctx context.Context, req CreateBookingRequest) (*CreateBookingResponse, error) {
	var out CreateBookingResponse
	if err := c.do(ctx, http.MethodPost, "/api/pss/bookings", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ProcessPayment captures payment on a booking, the precondition PSS
// enforces before a ticket can be issued. The idempotency key is derived
// from the PNR since generation only ever pays for a given PNR once.
func (c *Client) ProcessPayment(ctx context.Context, pnr string, amount float64) error {
	body := map[string]any{
		"amount":          amount,
		"payment_method":  "card",
		"idempotency_key": "tdms-gen-" + pnr,
	}
	return c.do(ctx, http.MethodPost, "/api/pss/bookings/"+url.PathEscape(pnr)+"/payment", body, nil)
}

// IssueTicket issues tickets for every passenger on the PNR, moving its
// live status to TICKETED — the "ticketed: true" Do.
func (c *Client) IssueTicket(ctx context.Context, pnr string) error {
	body := map[string]any{"passenger_id": "all"}
	return c.do(ctx, http.MethodPost, "/api/pss/bookings/"+url.PathEscape(pnr)+"/ticket", body, nil)
}
