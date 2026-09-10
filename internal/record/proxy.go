package record

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Proxy forwards requests to the real Emarsys API and records what went past.
//
// It is a plain forward proxy, not a man in the middle: the client is pointed
// at it, so there is no certificate to fake. WSSE survives the hop because the
// digest covers only the nonce, the timestamp and the secret -- not the host
// and not the body -- so an unmodified client authenticates upstream exactly as
// it would directly.
type Proxy struct {
	upstream *url.URL
	client   *http.Client
	writer   *Writer
	redactor *Redactor
	logger   *slog.Logger
	// MaxBody caps how much of a payload is read for recording.
	maxBody int64
}

type ProxyOptions struct {
	Upstream string
	Mode     Mode
	MaxBody  int64
	Logger   *slog.Logger
	Timeout  time.Duration
}

func NewProxy(w *Writer, opts ProxyOptions) (*Proxy, error) {
	upstream, err := url.Parse(opts.Upstream)
	if err != nil {
		return nil, fmt.Errorf("upstream %q: %w", opts.Upstream, err)
	}
	if upstream.Scheme == "" || upstream.Host == "" {
		return nil, fmt.Errorf("upstream %q needs a scheme and a host", opts.Upstream)
	}
	if opts.MaxBody <= 0 {
		opts.MaxBody = 16 << 20
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}

	// A per-session key, so pseudonyms are stable inside one recording and
	// cannot be correlated across recordings or reversed with a rainbow table.
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate session key: %w", err)
	}

	return &Proxy{
		upstream: upstream,
		client:   &http.Client{Timeout: opts.Timeout},
		writer:   w,
		redactor: NewRedactor(opts.Mode, key),
		logger:   opts.Logger,
		maxBody:  opts.MaxBody,
	}, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()

	requestBody, err := io.ReadAll(io.LimitReader(r.Body, p.maxBody))
	r.Body.Close()
	if err != nil {
		http.Error(w, "could not read request body", http.StatusBadRequest)
		return
	}

	target := *p.upstream
	target.Path = strings.TrimSuffix(p.upstream.Path, "/") + r.URL.Path
	target.RawQuery = r.URL.RawQuery

	outbound, err := http.NewRequestWithContext(
		r.Context(), r.Method, target.String(), bytes.NewReader(requestBody))
	if err != nil {
		http.Error(w, "could not build upstream request", http.StatusInternalServerError)
		return
	}
	for name, values := range r.Header {
		if strings.EqualFold(name, "Host") || strings.EqualFold(name, "Content-Length") {
			continue
		}
		for _, value := range values {
			outbound.Header.Add(name, value)
		}
	}

	exchange := Exchange{
		Timestamp:      started.UTC(),
		Method:         r.Method,
		Path:           r.URL.Path,
		Query:          flattenQuery(r.URL.Query()),
		RequestHeaders: p.redactor.Headers(r.Header),
		RequestBody:    p.redactor.Body(r.Header.Get("Content-Type"), requestBody),
		Redaction:      p.redactor.Mode,
	}

	resp, err := p.client.Do(outbound)
	if err != nil {
		exchange.Error = err.Error()
		exchange.DurationMS = time.Since(started).Milliseconds()
		p.record(exchange)

		// A failed upstream call is still worth recording, and the client
		// should see a real error rather than a hang.
		http.Error(w, "upstream request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, p.maxBody))
	if err != nil {
		exchange.Error = "could not read upstream response: " + err.Error()
	}

	exchange.Status = resp.StatusCode
	exchange.ResponseHeaders = p.redactor.Headers(resp.Header)
	exchange.ResponseBody = p.redactor.Body(resp.Header.Get("Content-Type"), responseBody)
	exchange.DurationMS = time.Since(started).Milliseconds()
	p.record(exchange)

	for name, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(responseBody)
}

// record never fails the exchange: the client is talking to production, and a
// recording problem must not cost a real call.
func (p *Proxy) record(e Exchange) {
	if err := p.writer.Write(e); err != nil {
		p.logger.Error("could not record exchange", "path", e.Path, "err", err)
		return
	}
	p.logger.Info("recorded",
		"method", e.Method, "path", e.Path, "status", e.Status, "ms", e.DurationMS)
}

func flattenQuery(values url.Values) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, list := range values {
		out[key] = strings.Join(list, ",")
	}
	return out
}
