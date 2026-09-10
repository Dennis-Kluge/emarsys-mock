package api

import (
	"encoding/json"
	"net/http"
)

// Envelope is the response body of every Suite API call.
//
// Data carries an object on success. On a hard error Emarsys degrades it to a
// string rather than null, and clients that unmarshal into a struct depend on
// that, so Error writes an empty string there.
type Envelope struct {
	ReplyCode ReplyCode `json:"replyCode"`
	ReplyText string    `json:"replyText"`
	Data      any       `json:"data"`
}

// contextKey namespaces the values middleware attaches to a request.
type contextKey string

// ReplyCodeContextKey lets the request logger record the reply code a handler
// produced without every handler having to report it explicitly.
const ReplyCodeContextKey contextKey = "reply-code"

// ReplyCodeRecorder is implemented by the logging ResponseWriter wrapper.
type ReplyCodeRecorder interface {
	RecordReplyCode(ReplyCode)
}

// OK writes a successful envelope with the given payload.
func OK(w http.ResponseWriter, data any) {
	write(w, http.StatusOK, Envelope{ReplyCode: CodeOK, ReplyText: "OK", Data: data})
}

// Error writes a failure envelope using the code's canonical text and status.
func Error(w http.ResponseWriter, code ReplyCode) {
	ErrorText(w, code, code.Text())
}

// ErrorText writes a failure envelope with a custom reply text. Most Emarsys
// error messages interpolate the offending value, e.g.
// "No contact found with the external id: jane@example.com".
func ErrorText(w http.ResponseWriter, code ReplyCode, text string) {
	write(w, code.Status(), Envelope{ReplyCode: code, ReplyText: text, Data: ""})
}

// ErrorStatus writes a failure envelope with an explicit HTTP status, for the
// cases where the transport status and the reply code diverge (429, 403).
func ErrorStatus(w http.ResponseWriter, status int, code ReplyCode, text string) {
	write(w, status, Envelope{ReplyCode: code, ReplyText: text, Data: ""})
}

func write(w http.ResponseWriter, status int, env Envelope) {
	if rec, ok := w.(ReplyCodeRecorder); ok {
		rec.RecordReplyCode(env.ReplyCode)
	}
	body, err := json.Marshal(env)
	if err != nil {
		// Marshalling an Envelope can only fail if a handler put an
		// unmarshalable value in Data, which is a bug in that handler.
		body = []byte(`{"replyCode":2011,"replyText":"Internal error","data":""}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	w.WriteHeader(status)
	w.Write(body)
}
