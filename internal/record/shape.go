package record

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// A Shape is a payload's structure with the values taken out. Merging the
// shapes of many recorded calls is what turns a pile of traffic into something
// a handler can be written against.
type Shape struct {
	// Kinds is the set of JSON types seen in this position. More than one entry
	// is the interesting case: it means the API is inconsistent, which is
	// exactly the kind of detail that breaks a client.
	Kinds map[string]bool
	// Fields is set for objects. Optional keys show up as fields present in
	// fewer samples than the object itself.
	Fields map[string]*Shape
	// Elem is set for arrays.
	Elem *Shape
	// Seen counts how many samples reached this position.
	Seen int
}

func newShape() *Shape {
	return &Shape{Kinds: map[string]bool{}, Fields: map[string]*Shape{}}
}

// Observe folds one decoded value into the shape.
func (s *Shape) Observe(v any) {
	s.Seen++
	switch typed := v.(type) {
	case map[string]any:
		s.Kinds["object"] = true
		for key, child := range typed {
			field, ok := s.Fields[key]
			if !ok {
				field = newShape()
				s.Fields[key] = field
			}
			field.Observe(child)
		}
	case []any:
		s.Kinds["array"] = true
		if s.Elem == nil {
			s.Elem = newShape()
		}
		for _, child := range typed {
			s.Elem.Observe(child)
		}
	case string:
		s.Kinds["string"] = true
	case float64:
		if typed == float64(int64(typed)) {
			s.Kinds["integer"] = true
		} else {
			s.Kinds["number"] = true
		}
	case bool:
		s.Kinds["boolean"] = true
	case nil:
		s.Kinds["null"] = true
	}
}

// Describe renders the shape as indented text.
func (s *Shape) Describe(indent string, parentSeen int) string {
	if s == nil {
		return "(never present)"
	}

	kinds := make([]string, 0, len(s.Kinds))
	for kind := range s.Kinds {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	label := strings.Join(kinds, " | ")

	// A field that appears in only some samples is optional, and knowing which
	// fields are optional is half of writing a handler.
	if parentSeen > 0 && s.Seen < parentSeen {
		label += fmt.Sprintf("  (optional, %d/%d)", s.Seen, parentSeen)
	}

	var b strings.Builder
	b.WriteString(label)

	if len(s.Fields) > 0 {
		keys := make([]string, 0, len(s.Fields))
		for key := range s.Fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			b.WriteString("\n" + indent + "  " + key + ": ")
			b.WriteString(s.Fields[key].Describe(indent+"  ", s.Seen))
		}
	}
	if s.Elem != nil {
		b.WriteString("\n" + indent + "  []: ")
		b.WriteString(s.Elem.Describe(indent+"  ", 0))
	}
	return b.String()
}

// Endpoint aggregates every recorded call to one method and path template.
type Endpoint struct {
	Method   string
	Template string
	Calls    int
	Statuses map[int]int
	Request  *Shape
	Response *Shape
	// Examples keeps a couple of paths as they were actually called, so a
	// template with a wildcard can be read back to something concrete.
	Examples []string
}

var numericSegment = regexp.MustCompile(`^\d+$`)

// PathTemplate collapses id segments so that fifty calls to fifty contacts
// group into one endpoint.
func PathTemplate(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if numericSegment.MatchString(segment) {
			segments[i] = "{id}"
		}
	}
	return strings.Join(segments, "/")
}

// Summarize groups a recording into endpoints.
func Summarize(exchanges []Exchange) []Endpoint {
	byKey := map[string]*Endpoint{}

	for _, e := range exchanges {
		template := PathTemplate(e.Path)
		key := e.Method + " " + template

		endpoint, ok := byKey[key]
		if !ok {
			endpoint = &Endpoint{
				Method:   e.Method,
				Template: template,
				Statuses: map[int]int{},
				Request:  newShape(),
				Response: newShape(),
			}
			byKey[key] = endpoint
		}

		endpoint.Calls++
		endpoint.Statuses[e.Status]++
		if len(endpoint.Examples) < 3 && !contains(endpoint.Examples, e.Path) {
			endpoint.Examples = append(endpoint.Examples, e.Path)
		}
		observeJSON(endpoint.Request, e.RequestBody)
		observeJSON(endpoint.Response, e.ResponseBody)
	}

	out := make([]Endpoint, 0, len(byKey))
	for _, endpoint := range byKey {
		out = append(out, *endpoint)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Template != out[j].Template {
			return out[i].Template < out[j].Template
		}
		return out[i].Method < out[j].Method
	})
	return out
}

func observeJSON(shape *Shape, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return
	}
	shape.Observe(decoded)
}

// Report renders a summary meant to be read next to an editor while writing
// handlers.
func Report(endpoints []Endpoint) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%d endpoints recorded\n\n", len(endpoints))
	for _, endpoint := range endpoints {
		fmt.Fprintf(&b, "%s\n", strings.Repeat("=", 74))
		fmt.Fprintf(&b, "%s %s\n", endpoint.Method, endpoint.Template)
		fmt.Fprintf(&b, "  calls: %d   statuses: %s\n", endpoint.Calls, formatStatuses(endpoint.Statuses))
		if len(endpoint.Examples) > 0 {
			fmt.Fprintf(&b, "  seen as: %s\n", strings.Join(endpoint.Examples, ", "))
		}
		if endpoint.Request.Seen > 0 {
			fmt.Fprintf(&b, "\n  request:  %s\n", endpoint.Request.Describe("  ", 0))
		}
		if endpoint.Response.Seen > 0 {
			fmt.Fprintf(&b, "\n  response: %s\n", endpoint.Response.Describe("  ", 0))
		}
		b.WriteString("\n")
	}

	if mixed := mixedTypeFields(endpoints); len(mixed) > 0 {
		b.WriteString(strings.Repeat("=", 74) + "\n")
		b.WriteString("Fields that came back as more than one JSON type\n")
		b.WriteString("These are where clients break: whichever type a client saw first is the\n")
		b.WriteString("one it was written against.\n\n")
		for _, line := range mixed {
			b.WriteString("  " + line + "\n")
		}
	}
	return b.String()
}

// mixedTypeFields finds positions the API answered with inconsistent types.
func mixedTypeFields(endpoints []Endpoint) []string {
	var out []string
	for _, endpoint := range endpoints {
		walkShape(endpoint.Response, "response", func(path string, shape *Shape) {
			if scalarKindCount(shape) > 1 {
				kinds := make([]string, 0, len(shape.Kinds))
				for kind := range shape.Kinds {
					kinds = append(kinds, kind)
				}
				sort.Strings(kinds)
				out = append(out, fmt.Sprintf("%s %s  %s: %s",
					endpoint.Method, endpoint.Template, path, strings.Join(kinds, " | ")))
			}
		})
	}
	sort.Strings(out)
	return out
}

// scalarKindCount ignores null, which is a normal absence rather than an
// inconsistency, and counts the remaining types.
func scalarKindCount(s *Shape) int {
	n := 0
	for kind := range s.Kinds {
		if kind != "null" {
			n++
		}
	}
	return n
}

func walkShape(s *Shape, path string, visit func(string, *Shape)) {
	if s == nil {
		return
	}
	visit(path, s)
	keys := make([]string, 0, len(s.Fields))
	for key := range s.Fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		walkShape(s.Fields[key], path+"."+key, visit)
	}
	walkShape(s.Elem, path+"[]", visit)
}

func formatStatuses(statuses map[int]int) string {
	codes := make([]int, 0, len(statuses))
	for code := range statuses {
		codes = append(codes, code)
	}
	sort.Ints(codes)

	parts := make([]string, 0, len(codes))
	for _, code := range codes {
		parts = append(parts, strconv.Itoa(code)+"×"+strconv.Itoa(statuses[code]))
	}
	return strings.Join(parts, ", ")
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
