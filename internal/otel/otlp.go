package otel

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Edge is one parent-to-child server span observed during a fault window.
type Edge struct {
	ID        string
	From      string
	To        string
	At        time.Time
	LatencyMs float64
}

type payload struct {
	ResourceSpans []resourceSpan `json:"resourceSpans"`
}

type resourceSpan struct {
	Resource struct {
		Attributes []attribute `json:"attributes"`
	} `json:"resource"`
	ScopeSpans []struct {
		Spans []span `json:"spans"`
	} `json:"scopeSpans"`
}

type span struct {
	SpanID            string          `json:"spanId"`
	ParentSpanID      string          `json:"parentSpanId"`
	Name              string          `json:"name"`
	Kind              json.RawMessage `json:"kind"`
	StartTimeUnixNano string          `json:"startTimeUnixNano"`
	EndTimeUnixNano   string          `json:"endTimeUnixNano"`
}

type attribute struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue"`
	} `json:"value"`
}

type timedSpan struct {
	id      string
	parent  string
	service string
	start   time.Time
	end     time.Time
	server  bool
}

// Edges reads an OTLP HTTP JSON trace export.
// Only server spans with a parent in the same payload become edges, and only when the span overlaps the fault window.
func Edges(body []byte, windowStart, windowEnd time.Time) ([]Edge, error) {
	if windowStart.IsZero() {
		return nil, nil
	}
	var doc payload
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("otlp json: %w", err)
	}
	var spans []timedSpan
	for _, rs := range doc.ResourceSpans {
		service := serviceName(rs.Resource.Attributes)
		for _, scope := range rs.ScopeSpans {
			for _, sp := range scope.Spans {
				start, err := unixNano(sp.StartTimeUnixNano)
				if err != nil {
					continue
				}
				end, err := unixNano(sp.EndTimeUnixNano)
				if err != nil || end.Before(start) {
					end = start
				}
				spans = append(spans, timedSpan{
					id: sp.SpanID, parent: sp.ParentSpanID, service: service,
					start: start, end: end, server: isServer(sp.Kind),
				})
			}
		}
	}
	byID := map[string]timedSpan{}
	for _, sp := range spans {
		if sp.id != "" {
			byID[sp.id] = sp
		}
	}
	var out []Edge
	for _, sp := range spans {
		if !sp.server || sp.parent == "" || sp.service == "" {
			continue
		}
		parent, ok := byID[sp.parent]
		if !ok || parent.service == "" || parent.service == sp.service {
			continue
		}
		if !overlaps(sp.start, sp.end, windowStart, windowEnd) {
			continue
		}
		at := sp.start
		if at.Before(windowStart) {
			at = windowStart
		}
		out = append(out, Edge{
			ID: sp.id, From: parent.service, To: sp.service, At: at,
			LatencyMs: float64(sp.end.Sub(sp.start).Microseconds()) / 1000,
		})
	}
	return out, nil
}

func serviceName(attrs []attribute) string {
	for _, attr := range attrs {
		if attr.Key == "service.name" && attr.Value.StringValue != "" {
			return attr.Value.StringValue
		}
	}
	return ""
}

func isServer(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var num int
	if err := json.Unmarshal(raw, &num); err == nil {
		return num == 2
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text == "SPAN_KIND_SERVER" || text == "SERVER"
	}
	return false
}

func unixNano(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, fmt.Errorf("missing time")
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, n).UTC(), nil
}

func overlaps(start, end, windowStart, windowEnd time.Time) bool {
	if !end.After(windowStart) {
		return false
	}
	if !windowEnd.IsZero() && !start.Before(windowEnd) {
		return false
	}
	return true
}
