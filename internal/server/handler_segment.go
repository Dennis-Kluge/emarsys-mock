package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/dennis-kluge/emarsys-mock/internal/api"
	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

// Segments are served as a CRUD facade over a static member list. Nothing here
// evaluates criteria: the mock does not run the segmentation engine, and a
// segment that is subtly wrong would be worse than one that is obviously
// static. Populate the members from the dashboard or the control plane.

// handleSegmentCreate serves PUT /api/v2/filter.
//
// Note the method: segments are created with PUT, not POST.
func (s *Server) handleSegmentCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string          `json:"name"`
		Criteria json.RawMessage `json:"criteria"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		api.ErrorText(w, api.CodeMissingKeyField, "Segment name must not be empty")
		return
	}

	id, err := s.db.CreateSegment(r.Context(), body.Name, string(body.Criteria))
	if err != nil {
		s.internalError(w, "create segment", err)
		return
	}
	api.OK(w, map[string]any{"id": id})
}

// handleSegmentGet serves GET /api/v2/filter/{segmentId}.
func (s *Server) handleSegmentGet(w http.ResponseWriter, r *http.Request) {
	segment, ok := s.lookupSegment(w, r)
	if !ok {
		return
	}
	api.OK(w, map[string]any{
		"id":      segment.ID,
		"name":    segment.Name,
		"created": s.localTime(segment.Created),
	})
}

// handleSegmentList serves GET /api/v2/filter.
func (s *Server) handleSegmentList(w http.ResponseWriter, r *http.Request) {
	segments, err := s.db.Segments(r.Context())
	if err != nil {
		s.internalError(w, "list segments", err)
		return
	}
	out := make([]map[string]any, 0, len(segments))
	for _, seg := range segments {
		out = append(out, map[string]any{"id": seg.ID, "name": seg.Name})
	}
	api.OK(w, out)
}

// handleSegmentDelete serves GET /api/v2/filter/{segmentId}/delete.
//
// A GET that mutates is unusual, but it is what production exposes.
func (s *Server) handleSegmentDelete(w http.ResponseWriter, r *http.Request) {
	segment, ok := s.lookupSegment(w, r)
	if !ok {
		return
	}
	if err := s.db.DeleteSegment(r.Context(), segment.ID); err != nil {
		s.internalError(w, "delete segment", err)
		return
	}
	api.OK(w, "")
}

// handleSegmentCount serves GET /api/v2/filter/{segmentId}/contacts/count.
func (s *Server) handleSegmentCount(w http.ResponseWriter, r *http.Request) {
	segment, ok := s.lookupSegment(w, r)
	if !ok {
		return
	}
	members, err := s.db.SegmentMembers(r.Context(), segment.ID)
	if err != nil {
		s.internalError(w, "read segment members", err)
		return
	}
	api.OK(w, len(members))
}

// handleSegmentContact serves GET /api/v2/filter/{segmentId}/contacts/{contactId},
// which answers whether one contact is in the segment.
func (s *Server) handleSegmentContact(w http.ResponseWriter, r *http.Request) {
	segment, ok := s.lookupSegment(w, r)
	if !ok {
		return
	}
	contactID, ok := pathInt64(w, r, "contactId")
	if !ok {
		return
	}
	members, err := s.db.SegmentMembers(r.Context(), segment.ID)
	if err != nil {
		s.internalError(w, "read segment members", err)
		return
	}
	for _, id := range members {
		if id == contactID {
			api.OK(w, true)
			return
		}
	}
	api.OK(w, false)
}

func (s *Server) lookupSegment(w http.ResponseWriter, r *http.Request) (store.Segment, bool) {
	id, ok := pathInt64(w, r, "segmentId")
	if !ok {
		return store.Segment{}, false
	}
	segment, err := s.db.Segment(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNoSegment) {
			api.ErrorText(w, api.CodeInternalError,
				"Unknown segment id: "+strconv.FormatInt(id, 10))
			return store.Segment{}, false
		}
		s.internalError(w, "load segment", err)
		return store.Segment{}, false
	}
	return segment, true
}
