package faultproxy

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"strings"
	"unicode/utf8"
)

func (r *UA1MailReceiver) smtpCorrelation(raw []byte) (map[string]any, bool, error) {
	if len(raw) == 0 || len(raw) > 65536 {
		return nil, false, ErrProtocol
	}
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, false, ErrProtocol
	}
	headers := http.Header(message.Header)
	invitation := headers.Get("X-Umir-Fixture-Invitation-ID")
	if len(headers.Values("X-Umir-Fixture-Invitation-ID")) == 0 {
		return smtpCorrelation(raw)
	}
	for _, name := range []string{"X-Umir-Fixture-Job-ID", "X-Umir-Fixture-Invitation-ID", "Message-ID", "From", "To", "Content-Type", "MIME-Version"} {
		if len(headers.Values(name)) != 1 {
			return nil, false, ErrProtocol
		}
	}
	if len(headers.Values("X-Umir-Fixture-Challenge-ID")) != 0 || len(headers.Values("X-Umir-Fixture-Submission-ID")) != 0 || headers.Get("MIME-Version") != "1.0" {
		return nil, false, ErrProtocol
	}
	job := headers.Get("X-Umir-Fixture-Job-ID")
	correlation := map[string]any{"kind": "auth-invitation-link", "jobID": job, "invitationID": invitation}
	if validateUA1Mail("LinkCorrelation", canonical(correlation)) != nil || headers.Get("Message-ID") != "<invitation."+job+"@umir.invalid>" {
		return nil, false, ErrProtocol
	}
	for _, name := range []string{"From", "To", "Cc", "Bcc", "Reply-To"} {
		for _, value := range headers.Values(name) {
			addresses, err := mail.ParseAddressList(value)
			if err != nil {
				return nil, false, ErrProtocol
			}
			for _, address := range addresses {
				if !syntheticAddress(address.Address) {
					return nil, false, ErrProtocol
				}
			}
		}
	}
	parts := []string{}
	budget := ua1MIMEBudget{}
	if err := ua1MIMEText(headers.Get("Content-Type"), headers.Get("Content-Transfer-Encoding"), message.Body, &parts, &budget, 0); err != nil || len(parts) == 0 {
		return nil, false, ErrProtocol
	}
	for _, text := range parts {
		if !strings.Contains(text, "/invite/"+invitation) {
			return nil, false, ErrProtocol
		}
	}
	return correlation, true, nil
}

type ua1MIMEBudget struct{ parts, bytes int }

func ua1MIMEText(contentType, transfer string, reader io.Reader, text *[]string, budget *ua1MIMEBudget, depth int) error {
	if depth > 4 || budget.parts >= 16 {
		return ErrProtocol
	}
	budget.parts++
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ErrProtocol
	}
	switch strings.ToLower(transfer) {
	case "", "7bit", "8bit":
	case "base64":
		reader = base64.NewDecoder(base64.StdEncoding, reader)
	case "quoted-printable":
		reader = quotedprintable.NewReader(reader)
	default:
		return ErrProtocol
	}
	if media == "multipart/related" || media == "multipart/alternative" {
		if params["boundary"] == "" {
			return ErrProtocol
		}
		parts := multipart.NewReader(reader, params["boundary"])
		for {
			part, err := parts.NextPart()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return ErrProtocol
			}
			err = ua1MIMEText(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part, text, budget, depth+1)
			err = errors.Join(err, part.Close())
			if err != nil {
				return ErrProtocol
			}
		}
	}
	data, err := io.ReadAll(io.LimitReader(reader, 65537))
	if err != nil || budget.bytes+len(data) > 65536 {
		return ErrProtocol
	}
	budget.bytes += len(data)
	if media == "image/png" {
		if _, err := png.DecodeConfig(bytes.NewReader(data)); err != nil {
			return ErrProtocol
		}
		return nil
	}
	if media != "text/plain" && media != "text/html" || !utf8.Valid(data) || len(bytes.TrimSpace(data)) == 0 {
		return ErrProtocol
	}
	if charset := strings.ToLower(params["charset"]); charset != "" && charset != "utf-8" && charset != "us-ascii" {
		return ErrProtocol
	}
	*text = append(*text, string(data))
	return nil
}
