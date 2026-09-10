package api

import (
	"net/http/httptest"
	"testing"
)

// TestEnvelopeShapes pins the exact JSON clients parse. Field order, the
// replyCode/replyText/data key names and the fact that a hard error degrades
// data to an empty string rather than null are all part of the contract.
func TestEnvelopeShapes(t *testing.T) {
	cases := []struct {
		name       string
		write      func(w *httptest.ResponseRecorder)
		wantStatus int
		wantBody   string
	}{
		{
			name:       "success with object payload",
			write:      func(w *httptest.ResponseRecorder) { OK(w, map[string]any{"id": 42}) },
			wantStatus: 200,
			wantBody:   `{"replyCode":0,"replyText":"OK","data":{"id":42}}`,
		},
		{
			name: "batch partial failure is still a success envelope",
			write: func(w *httptest.ResponseRecorder) {
				OK(w, map[string]any{
					"ids": []int{123},
					"errors": map[string]map[string]string{
						"jane@example.com": {"2008": "No contact found with the external id: jane@example.com"},
					},
				})
			},
			wantStatus: 200,
			wantBody: `{"replyCode":0,"replyText":"OK","data":{"errors":{"jane@example.com":` +
				`{"2008":"No contact found with the external id: jane@example.com"}},"ids":[123]}}`,
		},
		{
			name:       "hard error",
			write:      func(w *httptest.ResponseRecorder) { Error(w, CodeInvalidKeyFieldID) },
			wantStatus: 400,
			wantBody:   `{"replyCode":2004,"replyText":"Invalid key field id","data":""}`,
		},
		{
			name: "hard error with interpolated text",
			write: func(w *httptest.ResponseRecorder) {
				ErrorText(w, CodeNoContactFound, "No contact found with the external id: jane@example.com")
			},
			wantStatus: 400,
			wantBody: `{"replyCode":2008,"replyText":"No contact found with the external id: ` +
				`jane@example.com","data":""}`,
		},
		{
			name:       "unauthorized",
			write:      func(w *httptest.ResponseRecorder) { Error(w, CodeUnauthorized) },
			wantStatus: 401,
			wantBody:   `{"replyCode":1,"replyText":"Unauthorized","data":""}`,
		},
		{
			name: "status diverging from the code",
			write: func(w *httptest.ResponseRecorder) {
				ErrorStatus(w, 429, CodeInternalError, "Rate limit exceeded")
			},
			wantStatus: 429,
			wantBody:   `{"replyCode":2011,"replyText":"Rate limit exceeded","data":""}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.write(rec)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := rec.Body.String(); got != tc.wantBody {
				t.Errorf("body  = %s\nwant   = %s", got, tc.wantBody)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json;charset=utf-8" {
				t.Errorf("Content-Type = %q", ct)
			}
		})
	}
}

func TestReplyCodeTable(t *testing.T) {
	cases := []struct {
		code       ReplyCode
		wantStatus int
	}{
		{CodeOK, 200},
		{CodeUnauthorized, 401},
		{CodeBatchTooLarge, 400},
		{CodeInvalidKeyFieldID, 400},
		{CodeMultipleContacts, 400},
		{CodeInternalError, 500},
		{CodeNoIndexOnColumn, 400},
		{CodeInvalidLimit, 400},
	}
	for _, tc := range cases {
		if got := tc.code.Status(); got != tc.wantStatus {
			t.Errorf("ReplyCode(%d).Status() = %d, want %d", tc.code, got, tc.wantStatus)
		}
		if tc.code.Text() == "" {
			t.Errorf("ReplyCode(%d) has no reply text", tc.code)
		}
	}

	// An unknown code must still render something sane rather than panic.
	unknown := ReplyCode(9999)
	if unknown.Text() != "Unknown error" || unknown.Status() != 400 {
		t.Errorf("unknown code = %q / %d", unknown.Text(), unknown.Status())
	}
}

type recordingWriter struct {
	*httptest.ResponseRecorder
	got ReplyCode
}

func (r *recordingWriter) RecordReplyCode(c ReplyCode) { r.got = c }

func TestEnvelopeReportsReplyCodeToRecorder(t *testing.T) {
	// The request logger relies on this hook to record what each handler
	// answered without every handler having to report it explicitly.
	w := &recordingWriter{ResponseRecorder: httptest.NewRecorder()}
	Error(w, CodeMultipleContacts)
	if w.got != CodeMultipleContacts {
		t.Errorf("recorded reply code = %d, want %d", w.got, CodeMultipleContacts)
	}
}
