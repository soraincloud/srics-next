package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/soraincloud/srics-next/internal/atomicfile"
	"github.com/soraincloud/srics-next/internal/server"
	"github.com/soraincloud/srics-next/internal/verification"
	"github.com/soraincloud/srics-next/internal/webui"
)

var version = "0.1.0-dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "srics:", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if len(args) == 0 {
		args = []string{"serve"}
	}
	switch args[0] {
	case "version":
		fmt.Println("SRICS Next", version)
		return nil
	case "verify":
		flags := flag.NewFlagSet("verify", flag.ContinueOnError)
		reportPath := flags.String("report", "", "write a new JSON report (never overwrites)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected arguments")
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		report := verification.Run(ctx, verification.ResolveTools(), nil)
		if *reportPath != "" {
			if err := atomicfile.WriteNew(*reportPath, func(w io.Writer) error { return json.NewEncoder(w).Encode(report) }); err != nil {
				return err
			}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
		if report.Status != "passed" {
			return errors.New("verification failed; see report")
		}
		return nil
	case "serve":
		flags := flag.NewFlagSet("serve", flag.ContinueOnError)
		address := flags.String("addr", "127.0.0.1:19473", "loopback listen address")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected arguments")
		}
		host, _, err := net.SplitHostPort(*address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errors.New("M0 only permits loopback access; LAN login and HTTPS are not implemented yet")
		}
		listener, err := net.Listen("tcp", *address)
		if err != nil {
			return err
		}
		app := server.New(ctx, listener.Addr().String(), webui.Files(), func(ctx context.Context, publish func(verification.Report)) verification.Report {
			return verification.Run(ctx, verification.ResolveTools(), publish)
		})
		srv := &http.Server{Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
		done := make(chan struct{})
		go func() {
			defer close(done)
			<-ctx.Done()
			stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			srv.Shutdown(stop)
		}()
		fmt.Printf("SRICS Next %s · M0 本机验证版\nhttp://%s\n仅处理合成样本；按 Ctrl+C 停止。\n", version, listener.Addr())
		err = srv.Serve(listener)
		cancel()
		<-done
		app.Wait()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	default:
		return errors.New("usage: srics [serve | verify [--report new.json] | version]")
	}
}
