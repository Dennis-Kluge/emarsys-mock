// Command emarsys-record captures real Emarsys traffic so that mock handlers
// can be written against what production actually sends.
//
// It runs in front of an integration:
//
//	emarsys-record -listen :8081 -out emarsys.jsonl
//	# point the integration at http://localhost:8081 instead of api.emarsys.net
//
// and afterwards turns the capture into something readable:
//
//	emarsys-record -summarize emarsys.jsonl
//
// The documentation is less precise than the real API in several places, so a
// recording is the better source. It is also traffic from a production account,
// which is why values are pseudonymised unless raw mode is asked for
// explicitly.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dennis-kluge/emarsys-mock/internal/record"
)

func main() {
	listen := flag.String("listen", ":8081", "address to listen on")
	upstream := flag.String("upstream", "https://api.emarsys.net", "the real API to forward to")
	out := flag.String("out", "emarsys-recording.jsonl", "file to append the recording to")
	mode := flag.String("mode", string(record.ModeShapes),
		"shapes (pseudonymise values, safe for production) or raw (verbatim, test accounts only)")
	summarize := flag.String("summarize", "", "read a recording and print the endpoint shapes, then exit")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if *summarize != "" {
		if err := runSummary(*summarize); err != nil {
			logger.Error("summary failed", "err", err)
			os.Exit(1)
		}
		return
	}

	if err := runProxy(*listen, *upstream, *out, record.Mode(*mode), logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func runSummary(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	exchanges, err := record.Read(file)
	if err != nil {
		return err
	}
	if len(exchanges) == 0 {
		return fmt.Errorf("%s contains no exchanges", path)
	}

	fmt.Printf("%d exchanges from %s\n", len(exchanges), path)
	if exchanges[0].Redaction == record.ModeRaw {
		fmt.Println("WARNING: this recording was captured in raw mode and contains real data")
	}
	fmt.Println()
	fmt.Print(record.Report(record.Summarize(exchanges)))
	return nil
}

func runProxy(listen, upstream, out string, mode record.Mode, logger *slog.Logger) error {
	if mode != record.ModeShapes && mode != record.ModeRaw {
		return fmt.Errorf("unknown mode %q: use shapes or raw", mode)
	}

	writer, err := record.NewWriter(out)
	if err != nil {
		return err
	}
	defer writer.Close()

	proxy, err := record.NewProxy(writer, record.ProxyOptions{
		Upstream: upstream,
		Mode:     mode,
		Logger:   logger,
	})
	if err != nil {
		return err
	}

	// Nobody should have to read the source to know what ends up in the file.
	printDisclosure(upstream, out, mode)

	srv := &http.Server{
		Addr:              listen,
		Handler:           proxy,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("recording", "listen", listen, "upstream", upstream, "out", out, "mode", mode)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("stopping; the recording is flushed after every exchange")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func printDisclosure(upstream, out string, mode record.Mode) {
	fmt.Fprintf(os.Stderr, `
emarsys-record forwards to %s and writes to %s

  Never written:  credentials. X-WSSE, Authorization and cookies are recorded
                  only as "<redacted: scheme>", and every header outside a small
                  allowlist is dropped rather than redacted.

`, upstream, out)

	switch mode {
	case record.ModeShapes:
		fmt.Fprint(os.Stderr, `  Mode "shapes":  structure, keys and value shapes are kept; values are
                  replaced with per-session pseudonyms. An address stays an
                  address and a date keeps its format, so the payload is still
                  useful for writing a handler. Reply codes and status literals
                  are kept verbatim, with addresses stripped out of them.

                  A non-JSON body -- a CSV export, for instance -- is recorded
                  as its size and content type only. An export is contact data
                  in bulk and there is no shape worth keeping.

`)
	case record.ModeRaw:
		fmt.Fprint(os.Stderr, `  Mode "raw":     PAYLOADS ARE RECORDED VERBATIM, INCLUDING PERSONAL DATA.
                  Only point this at an account you seeded yourself. Against a
                  production account the resulting file is a personal-data
                  export and has to be handled as one.

`)
	}
}
