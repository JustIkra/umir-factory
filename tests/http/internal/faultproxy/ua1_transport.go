package faultproxy

import (
	"context"
	"crypto/tls"
	"net"
)

type fixtureSMTPReceiver interface {
	smtpCorrelation([]byte) (map[string]any, bool, error)
	Accept(context.Context, map[string]any, []byte, bool, bool) (string, error)
}

type ordinarySMTPReceiver struct{ *FixtureSink }

func (ordinarySMTPReceiver) smtpCorrelation(raw []byte) (map[string]any, bool, error) {
	return smtpCorrelation(raw)
}

func ServeUA1SMTP(ctx context.Context, listener net.Listener, receiver *UA1MailReceiver, cert tls.Certificate, username, password string) error {
	if receiver == nil {
		return ErrProtocol
	}
	return serveFixtureSMTP(ctx, listener, receiver, cert, username, password)
}
