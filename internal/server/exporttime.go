package server

import "time"

// emarsysTimeLayout is how Emarsys renders a timestamp in an API payload or an
// export file: a local calendar time with no zone marker.
const emarsysTimeLayout = "2006-01-02 15:04:05"

// localTime renders a stored UTC timestamp in the configured export timezone.
//
// Emarsys renders timestamps in Vienna local time rather than UTC and gives no
// offset, so a client that parses them as UTC is one or two hours out all year.
// Reproducing that is the point; EXPORT_TIMEZONE exists to make it visible.
func (s *Server) localTime(stored string) string {
	ts, err := parseStoredTime(stored)
	if err != nil {
		return stored
	}
	return ts.In(s.cfg.ExportLocation).Format(emarsysTimeLayout)
}

func parseStoredTime(stored string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, emarsysTimeLayout} {
		if ts, err := time.Parse(layout, stored); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, errUnparsableTime
}

// parseRangeBound reads one end of a time_range.
//
// A bare date is interpreted in the export timezone, not in UTC, because that
// is the calendar the caller is thinking in when they ask for "yesterday".
func (s *Server) parseRangeBound(raw string, endOfDay bool) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, emarsysTimeLayout, "2006-01-02T15:04:05", "2006-01-02"} {
		ts, err := time.ParseInLocation(layout, raw, s.cfg.ExportLocation)
		if err != nil {
			continue
		}
		if layout == "2006-01-02" && endOfDay {
			ts = ts.Add(24*time.Hour - time.Second)
		}
		return ts.UTC(), true
	}
	return time.Time{}, false
}

type timeParseError struct{}

func (timeParseError) Error() string { return "unparsable timestamp" }

var errUnparsableTime = timeParseError{}
