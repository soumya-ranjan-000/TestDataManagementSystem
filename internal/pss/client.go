// Package pss is TDMS's adapter to the airline system it checks live PNRs
// against — here, the RAG-Chatbot-Project's PSS (Passenger Service
// System) mock backend. This is the "live" side of the design doc's
// health check: "TDMS looks instead: it reads the PNR's real state and
// decides."
package pss

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		BaseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: 20 * time.Second},
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

// Booking is PSS's response shape for GET /api/pss/bookings/{pnr},
// trimmed to the fields TDMS's dictionary currently references.
type Booking struct {
	PNR      string    `json:"pnr"`
	Status   string    `json:"status"`
	Segments []Segment `json:"segments"`
}

// GetBooking fetches the live state of one PNR.
func (c *Client) GetBooking(pnr string) (*Booking, error) {
	u := c.BaseURL + "/api/pss/bookings/" + url.PathEscape(pnr)
	resp, err := c.HTTPClient.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("pss API error %d: %s", resp.StatusCode, string(body))
	}
	var booking Booking
	if err := json.Unmarshal(body, &booking); err != nil {
		return nil, fmt.Errorf("decoding booking response: %w", err)
	}
	return &booking, nil
}
