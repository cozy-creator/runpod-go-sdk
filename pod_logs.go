package runpod

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PodLogSource selects the provider's container or platform log stream.
type PodLogSource string

const (
	PodLogSourceContainer PodLogSource = "container"
	PodLogSourceSystem    PodLogSource = "system"
)

// PodLogsOptions controls REST v2 replay. Omitted source includes both sources.
// Tail nil uses the provider default (100 per source); zero requests live-only
// events. LastEventID takes precedence over Since, which takes precedence over
// Tail. Cursors are opaque: preserve an entry's ID rather than rebuilding it.
type PodLogsOptions struct {
	Source      PodLogSource
	Tail        *int
	Since       time.Time
	LastEventID string
}

// PodLogEntry is one provider SSE data event. Line is diagnostic text, not an
// authoritative pod lifecycle state. ID is the exact SSE reconnect cursor.
// IDs and timestamps need not be unique; distinct lines can share both.
type PodLogEntry struct {
	ID        string       `json:"id"`
	Source    PodLogSource `json:"source"`
	Line      string       `json:"line"`
	Timestamp time.Time    `json:"ts"`
}

// PodLogStream owns the response body. Read with Next and always call Close.
// Next is sequential; Close may be called to interrupt a blocked read.
type PodLogStream struct {
	body   io.ReadCloser
	reader *bufio.Reader
	lastID string
}

// StreamPodLogs opens the read-only REST v2 SSE endpoint, using the same API key
// as the other methods. It can block until the first matching log: the provider
// may send no response headers when the requested history is empty.
//
// The stream has no replay-complete marker and does not end after Tail entries.
// The caller owns cancellation/snapshot policy. The configured HTTP client
// timeout applies to the entire stream; use WithTimeout(0) and a context for a
// long-lived stream. HTTP setup follows the normal retry policy; delivered
// streams are never silently reconnected or replayed by the SDK.
func (c *Client) StreamPodLogs(ctx context.Context, podID string, options *PodLogsOptions) (*PodLogStream, error) {
	if err := c.validateRequired("podID", podID); err != nil {
		return nil, err
	}
	query := url.Values{}
	headers := http.Header{"Accept": {"text/event-stream"}}
	if options != nil {
		if options.Source != "" && options.Source != PodLogSourceContainer && options.Source != PodLogSourceSystem {
			return nil, NewValidationError("source", "must be container, system, or omitted")
		}
		if options.Source != "" {
			query.Set("source", string(options.Source))
		}
		if options.Tail != nil {
			if *options.Tail < 0 || *options.Tail > 5000 {
				return nil, NewValidationError("tail", "must be between 0 and 5000")
			}
			query.Set("tail", strconv.Itoa(*options.Tail))
		}
		if !options.Since.IsZero() {
			query.Set("since", options.Since.Format(time.RFC3339Nano))
		}
		if strings.ContainsAny(options.LastEventID, "\r\n\x00") {
			return nil, NewValidationError("lastEventID", "must be a single header value")
		}
		if options.LastEventID != "" {
			headers.Set("Last-Event-ID", options.LastEventID)
		}
	}
	endpoint := strings.TrimRight(c.restV2BaseURL, "/") + "/pods/" + url.PathEscape(podID) + "/logs"
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	response, err := c.makeRequestBytesWithHeaders(ctx, http.MethodGet, endpoint, nil, headers)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		if response.StatusCode >= 400 {
			return nil, c.handleResponse(response, nil)
		}
		response.Body.Close()
		return nil, fmt.Errorf("unexpected pod log HTTP status: %d", response.StatusCode)
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "text/event-stream" {
		response.Body.Close()
		return nil, fmt.Errorf("pod logs require text/event-stream, got %q", response.Header.Get("Content-Type"))
	}
	return &PodLogStream{body: response.Body, reader: bufio.NewReader(response.Body)}, nil
}

// Next reads the next complete SSE data event. Heartbeats and other SSE fields
// are ignored. EOF and cancellation remain errors, never a successful empty
// snapshot. An interrupted, unterminated event is not returned as a log entry.
func (s *PodLogStream) Next() (*PodLogEntry, error) {
	var data strings.Builder
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF && (len(line) != 0 || data.Len() != 0) {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			if data.Len() == 0 {
				continue
			}
			var entry PodLogEntry
			if err := json.Unmarshal([]byte(data.String()), &entry); err != nil {
				return nil, fmt.Errorf("decode pod log event: %w", err)
			}
			if entry.Source == "" || entry.Timestamp.IsZero() {
				return nil, fmt.Errorf("pod log event is missing source or timestamp")
			}
			entry.ID = s.lastID
			return &entry, nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			if !strings.ContainsRune(value, '\x00') {
				s.lastID = value
			}
		case "data":
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(value)
		}
	}
}

// Close releases the stream and interrupts any blocked Next call.
func (s *PodLogStream) Close() error { return s.body.Close() }
