// Package notify emails run reports over SMTP. Each recipient gets at most
// one email per run, holding the sections they subscribed to: the scan
// summary, the generation report (when something was generated), and the
// error report (when something failed).
package notify

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"strings"
	"time"

	"github.com/soumya-ranjan-000/tdms/internal/storage"
)

type Mailer struct {
	Host, Port, User, Pass, From string
	// BaseURL, when set, adds a link to the run's page in each email.
	BaseURL string
}

// FromEnv returns a Mailer from SMTP_HOST/PORT/USER/PASS/FROM, or nil when
// SMTP_HOST is unset (email disabled).
func FromEnv() *Mailer {
	if os.Getenv("SMTP_HOST") == "" {
		return nil
	}
	port := os.Getenv("SMTP_PORT")
	if port == "" {
		port = "587"
	}
	return &Mailer{
		Host: os.Getenv("SMTP_HOST"), Port: port,
		User: os.Getenv("SMTP_USER"), Pass: os.Getenv("SMTP_PASS"),
		From: os.Getenv("SMTP_FROM"), BaseURL: strings.TrimRight(os.Getenv("TDMS_BASE_URL"), "/"),
	}
}

// Email is one composed message.
type Email struct {
	To, Subject, Body string
}

// Compose builds one email per recipient with the sections their flags
// select, skipping recipients for whom this run has nothing to say.
func Compose(team *storage.Team, run *storage.Run, items []storage.RunItem, recipients []storage.Recipient, baseURL string) []Email {
	var generated, problems []storage.RunItem
	for _, it := range items {
		switch it.Outcome {
		case storage.OutcomeGenerated, storage.OutcomeRegenerated:
			generated = append(generated, it)
		case storage.OutcomeGenerationFailed, storage.OutcomeCheckError, storage.OutcomeBlockError:
			problems = append(problems, it)
		}
	}
	hasErrors := len(problems) > 0 || run.SyncError != nil || run.Error != nil

	c := run.Counts
	subject := sanitizeHeader(fmt.Sprintf("[TDMS] %s %s run %s: %d valid, %d invalid, %d generated, %d errors",
		team.Name, run.Mode, run.Status, c.Valid, c.Invalid, c.Generated, c.Errors))

	var emails []Email
	for _, rc := range recipients {
		var b strings.Builder
		if rc.OnScan {
			writeScan(&b, run, items)
		}
		if rc.OnGeneration && len(generated) > 0 {
			writeGenerated(&b, generated)
		}
		if rc.OnError && hasErrors {
			writeProblems(&b, run, problems)
		}
		if b.Len() == 0 {
			continue
		}
		if baseURL != "" {
			fmt.Fprintf(&b, "\nFull report: %s/runs/%s\n", baseURL, run.ID)
		}
		emails = append(emails, Email{To: rc.Email, Subject: subject, Body: b.String()})
	}
	return emails
}

func writeScan(b *strings.Builder, run *storage.Run, items []storage.RunItem) {
	c := run.Counts
	fmt.Fprintf(b, "SCAN REPORT\n===========\n")
	fmt.Fprintf(b, "Mode: %s   Trigger: %s   Status: %s\n", run.Mode, run.Trigger, run.Status)
	fmt.Fprintf(b, "Checked %d: %d valid, %d invalid, %d generated, %d errors\n\n", c.Total, c.Valid, c.Invalid, c.Generated, c.Errors)
	listed := 0
	for _, it := range items {
		if it.Outcome == storage.OutcomeValid {
			continue
		}
		fmt.Fprintf(b, "  %-12s %-10s %-18s %s\n", it.TestCaseKey, it.Environment, it.Outcome, detail(it))
		listed++
	}
	if listed == 0 && len(items) > 0 {
		b.WriteString("  All test data is valid.\n")
	}
	b.WriteString("\n")
}

func writeGenerated(b *strings.Builder, generated []storage.RunItem) {
	fmt.Fprintf(b, "TEST DATA GENERATED\n===================\n")
	for _, it := range generated {
		old := "-"
		if it.OldPNR != nil {
			old = *it.OldPNR
		}
		fmt.Fprintf(b, "  %-12s %-10s %s -> %s\n", it.TestCaseKey, it.Environment, old, deref(it.NewPNR))
	}
	b.WriteString("\n")
}

func writeProblems(b *strings.Builder, run *storage.Run, problems []storage.RunItem) {
	fmt.Fprintf(b, "ERRORS\n======\n")
	if run.Error != nil {
		fmt.Fprintf(b, "  Run failed: %s\n", *run.Error)
	}
	if run.SyncError != nil {
		fmt.Fprintf(b, "  Sync from the test management app failed: %s\n", *run.SyncError)
	}
	for _, it := range problems {
		fmt.Fprintf(b, "  %-12s %-10s %-18s %s\n", it.TestCaseKey, it.Environment, it.Outcome, deref(it.Error))
	}
	b.WriteString("\n")
}

func detail(it storage.RunItem) string {
	parts := []string{}
	if it.FailedRule != nil {
		parts = append(parts, "rule="+*it.FailedRule)
	}
	if it.OldPNR != nil {
		parts = append(parts, "old="+*it.OldPNR)
	}
	if it.NewPNR != nil {
		parts = append(parts, "new="+*it.NewPNR)
	}
	if it.Error != nil {
		parts = append(parts, *it.Error)
	}
	return strings.Join(parts, " ")
}

func deref(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}

// sanitizeHeader strips CR/LF so no value can inject extra headers.
func sanitizeHeader(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

// Send delivers one email. Port 465 uses implicit TLS; any other port uses
// STARTTLS when the server offers it (net/smtp refuses to send PLAIN
// credentials over an unencrypted connection to a non-localhost server).
func (m *Mailer) Send(e Email) error {
	msg := strings.Join([]string{
		"From: " + sanitizeHeader(m.From),
		"To: " + sanitizeHeader(e.To),
		"Subject: " + sanitizeHeader(e.Subject),
		"Date: " + time.Now().Format(time.RFC1123Z),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		strings.ReplaceAll(e.Body, "\n", "\r\n"),
	}, "\r\n")

	var auth smtp.Auth
	if m.User != "" {
		auth = smtp.PlainAuth("", m.User, m.Pass, m.Host)
	}
	addr := net.JoinHostPort(m.Host, m.Port)
	if m.Port != "465" {
		return smtp.SendMail(addr, auth, m.From, []string{e.To}, []byte(msg))
	}

	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: m.Host})
	if err != nil {
		return err
	}
	client, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(m.From); err != nil {
		return err
	}
	if err := client.Rcpt(e.To); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}
