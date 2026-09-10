package record

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Exchange is one recorded request and its response.
type Exchange struct {
	Timestamp time.Time         `json:"timestamp"`
	Method    string            `json:"method"`
	Path      string            `json:"path"`
	Query     map[string]string `json:"query,omitempty"`

	RequestHeaders map[string]string `json:"request_headers,omitempty"`
	RequestBody    json.RawMessage   `json:"request_body,omitempty"`

	Status          int               `json:"status"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
	ResponseBody    json.RawMessage   `json:"response_body,omitempty"`

	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`

	// Redaction records how this exchange was captured, so nobody has to guess
	// later whether a file contains personal data.
	Redaction Mode `json:"redaction"`
}

// Writer appends exchanges to a JSONL file.
//
// One JSON object per line, flushed after every write: a recording session that
// is interrupted still leaves everything captured up to that point, which
// matters when the session is someone watching production traffic for an hour.
type Writer struct {
	mu   sync.Mutex
	file *os.File
	buf  *bufio.Writer
}

func NewWriter(path string) (*Writer, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open recording file: %w", err)
	}
	return &Writer{file: file, buf: bufio.NewWriter(file)}, nil
}

func (w *Writer) Write(e Exchange) error {
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode exchange: %w", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.buf.Write(append(line, '\n')); err != nil {
		return err
	}
	return w.buf.Flush()
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.buf.Flush(); err != nil {
		w.file.Close()
		return err
	}
	return w.file.Close()
}

// Read loads a recording.
func Read(r io.Reader) ([]Exchange, error) {
	scanner := bufio.NewScanner(r)
	// A contact batch is capped at 8 MB, so a single line can be large.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var out []Exchange
	line := 0
	for scanner.Scan() {
		line++
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var e Exchange
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		out = append(out, e)
	}
	return out, scanner.Err()
}
