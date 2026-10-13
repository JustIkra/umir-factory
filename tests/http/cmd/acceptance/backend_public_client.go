package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

type backendPublicClient struct {
	Jar       http.CookieJar
	Transport http.RoundTripper
	session   *backendSession
}

func (c *backendPublicClient) Do(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil || req.URL.Scheme+"://"+req.URL.Host != c.session.base || req.URL.User != nil || req.URL.Fragment != "" || req.URL.Opaque != "" {
		if req != nil {
			return nil, errors.Join(errors.New("public endpoint rejected"), closeBackendRequest(req))
		}
		return nil, errors.New("public endpoint rejected")
	}
	size := len(req.URL.RequestURI())
	for name, values := range req.Header {
		size += len(name)
		for _, value := range values {
			size += len(value)
		}
	}
	if size > 16384 || req.ContentLength > 1<<20 {
		return nil, errors.Join(errors.New("public request budget exceeded"), closeBackendRequest(req))
	}
	var raw []byte
	if req.Body != nil {
		var err error
		raw, err = io.ReadAll(io.LimitReader(req.Body, (1<<20)+1))
		err = errors.Join(err, req.Body.Close())
		if err != nil || len(raw) > 1<<20 {
			return nil, errors.New("public request unavailable")
		}
	}
	release := func() {}
	if c.session.probe != nil {
		var err error
		release, err = c.session.probe(req.Context())
		if err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
	request := req.Clone(ctx)
	if req.Body != nil {
		request.Body = io.NopCloser(bytes.NewReader(raw))
	}
	request.GetBody = nil
	client := &http.Client{Jar: c.Jar, Transport: c.Transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		cancel()
		release()
		return nil, errors.New("public transport outcome unknown")
	}
	body := &backendPublicBody{body: response.Body, remaining: 1 << 20, cancel: cancel, release: release}
	response.Body = body
	context.AfterFunc(ctx, body.close)
	if response.ContentLength > 1<<20 || response.StatusCode >= 300 && response.StatusCode < 400 {
		return nil, errors.Join(errors.New("public response rejected"), body.Close())
	}
	return response, nil
}

func closeBackendRequest(req *http.Request) error {
	if req.Body != nil {
		return req.Body.Close()
	}
	return nil
}

type backendPublicBody struct {
	body      io.ReadCloser
	remaining int
	cancel    context.CancelFunc
	release   func()
	closed    sync.Once
	closeErr  error
}

func (b *backendPublicBody) Read(p []byte) (int, error) {
	if len(p) > b.remaining+1 {
		p = p[:b.remaining+1]
	}
	n, err := b.body.Read(p)
	b.remaining -= n
	if b.remaining < 0 {
		return 0, errors.Join(errors.New("public response budget exceeded"), b.Close())
	}
	if err != nil {
		if closeErr := b.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}
	return n, err
}

func (b *backendPublicBody) Close() error {
	b.close()
	return b.closeErr
}

func (b *backendPublicBody) close() {
	b.closed.Do(func() {
		b.closeErr = b.body.Close()
		b.cancel()
		b.release()
	})
}
