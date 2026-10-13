package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/JustIkra/umir-factory/tests/http/internal/faultproxy"
)

var sinkRole string

func main() {
	if sinkRole != "" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if err := faultproxy.RunFixtureSink(ctx, sinkRole, os.Args[1:]); err != nil {
			writeDiagnostic(errors.New("synthetic sink unavailable"))
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if len(os.Args) != 3 || healthcheck(os.Args[2]) != nil {
			os.Exit(1)
		}
		return
	}
	if err := run(os.Args[1:]); err != nil {
		writeDiagnostic(err)
		os.Exit(1)
	}
}

func writeDiagnostic(err error) {
	if _, writeErr := fmt.Fprintln(os.Stderr, err); writeErr != nil {
		return
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("fault-proxy", flag.ContinueOnError)
	listen := flags.String("listen", ":8080", "upstream proxy listener")
	controlListen := flags.String("control-listen", "0.0.0.0:9090", "test control listener")
	upstream := flags.String("upstream", "", "upstream HTTP service URL")
	token := flags.String("control-token", "", "ephemeral runner control token")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *upstream == "" || *token == "" || !strings.Contains(*listen, ":") || !strings.Contains(*controlListen, ":") {
		return errors.New("--upstream, --control-token, --listen and --control-listen are required")
	}
	proxy, err := faultproxy.New(*upstream, *token)
	if err != nil {
		return err
	}
	mainServer := &http.Server{Addr: *listen, Handler: proxy, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 16 * time.Second, IdleTimeout: 30 * time.Second}
	controlServer := &http.Server{Addr: *controlListen, Handler: proxy.ControlHandler(), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 32 * time.Second, IdleTimeout: 30 * time.Second}
	errorsCh := make(chan error, 2)
	go serve(mainServer, errorsCh)
	go serve(controlServer, errorsCh)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
	case err := <-errorsCh:
		if err != nil {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return errors.Join(mainServer.Shutdown(shutdownCtx), controlServer.Shutdown(shutdownCtx))
}

func serve(server *http.Server, errorsCh chan<- error) {
	err := server.ListenAndServe()
	if !errors.Is(err, http.ErrServerClosed) {
		errorsCh <- err
	}
}

func healthcheck(target string) (resultErr error) {
	client := &http.Client{Timeout: 1 * time.Second}
	response, err := client.Get(target)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, response.Body.Close()) }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck got status %d", response.StatusCode)
	}
	return nil
}
