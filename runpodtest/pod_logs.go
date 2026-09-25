package runpodtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

// SetPodLogs seeds a fixed SSE replay, including duplicate IDs. Streams wait for
// cancellation after replay; they do not simulate a replay-complete marker.
// Since/cursors use inclusive timestamp bounds in this fixture. Production
// reconnect/retention guarantees must be verified against the actual provider.
func (s *Server) SetPodLogs(podID string, entries []runpod.PodLogEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.podLogs[podID] = append([]runpod.PodLogEntry(nil), entries...)
}

func (s *Server) handlePodLogs(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(strings.TrimPrefix(path, "/rest-v2/pods/"), "/")
	if r.Method != http.MethodGet || len(parts) != 2 || parts[1] != "logs" {
		writeErr(w, 404, "route not found")
		return
	}
	s.mu.Lock()
	_, exists := s.pods[parts[0]]
	rows, retained := s.podLogs[parts[0]]
	rows = append([]runpod.PodLogEntry(nil), rows...)
	s.mu.Unlock()
	if !exists && !retained {
		writeErr(w, 404, "pod logs not found")
		return
	}
	source := r.URL.Query().Get("source")
	if source != "" && source != "system" && source != "container" {
		writeErr(w, 400, "invalid source")
		return
	}
	tail := 100
	if value := r.URL.Query().Get("tail"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 || parsed > 5000 {
			writeErr(w, 400, "invalid tail")
			return
		}
		tail = parsed
	}
	since := r.URL.Query().Get("since")
	if cursor := r.Header.Get("Last-Event-ID"); cursor != "" {
		since, _, _ = strings.Cut(cursor, "/")
	}
	var start time.Time
	if since != "" {
		var err error
		start, err = time.Parse(time.RFC3339Nano, since)
		if err != nil {
			writeErr(w, 400, "invalid cursor")
			return
		}
	}
	selected := make([]runpod.PodLogEntry, 0, len(rows))
	counts := map[runpod.PodLogSource]int{}
	for i := len(rows) - 1; i >= 0; i-- {
		entry := rows[i]
		if source != "" && string(entry.Source) != source {
			continue
		}
		if !start.IsZero() {
			if entry.Timestamp.Before(start) {
				continue
			}
		} else if counts[entry.Source] >= tail {
			continue
		}
		counts[entry.Source]++
		selected = append(selected, entry)
	}
	if len(selected) > 0 {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := len(selected) - 1; i >= 0; i-- {
			entry := selected[i]
			body, _ := json.Marshal(struct {
				Source    runpod.PodLogSource `json:"source"`
				Line      string              `json:"line"`
				Timestamp time.Time           `json:"ts"`
			}{entry.Source, entry.Line, entry.Timestamp})
			fmt.Fprintf(w, "id: %s\ndata: %s\n\n", entry.ID, body)
		}
		w.(http.Flusher).Flush()
	}
	<-r.Context().Done()
}
