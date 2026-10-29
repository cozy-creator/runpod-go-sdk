package runpod_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

const volumeRow = `{"id":"v1","name":"models","size":100,"dataCenter":"dc1","type":"STANDARD"}`

func TestResourcePaginationPreservesOpaqueCursor(t *testing.T) {
	calls := 0
	client := newResourceClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			fmt.Fprintf(w, `{"networkVolumes":[%s],"pagination":{"nextCursor":"part+/=next&1","hasNextPage":true}}`, volumeRow)
			return
		}
		if r.URL.Query().Get("cursor") != "part+/=next&1" {
			t.Errorf("cursor=%q", r.URL.Query().Get("cursor"))
		}
		fmt.Fprint(w, `{"networkVolumes":[],"pagination":{"nextCursor":null,"hasNextPage":false}}`)
	})
	rows, err := client.ListNetworkVolumes(context.Background())
	if err != nil || len(rows) != 1 || calls != 2 {
		t.Fatalf("rows=%+v calls=%d err=%v", rows, calls, err)
	}
}
func TestResourcePaginationRefusesIncompleteSnapshots(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"networkVolumes":null,"pagination":{"nextCursor":null,"hasNextPage":false}}`, `{"networkVolumes":[]}`, `{"networkVolumes":[],"pagination":{"nextCursor":null}}`, `{"networkVolumes":[],"pagination":{"hasNextPage":false}}`, `{"networkVolumes":[],"pagination":{"nextCursor":42,"hasNextPage":false}}`, `{"networkVolumes":[],"pagination":{"nextCursor":"next","hasNextPage":false}}`, `{"networkVolumes":[],"pagination":{"nextCursor":"next","hasNextPage":true}}`, `{"networkVolumes":[` + volumeRow + `],"pagination":{"nextCursor":null,"hasNextPage":true}}`} {
		t.Run(body, func(t *testing.T) {
			client := newResourceClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			if rows, err := client.ListNetworkVolumes(context.Background()); err == nil || rows != nil {
				t.Fatalf("accepted incomplete snapshot rows=%+v err=%v", rows, err)
			}
		})
	}
}
func TestResourcePaginationRejectsCyclesAndRepeatedIDs(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		t.Run(fmt.Sprint(cycle), func(t *testing.T) {
			calls := 0
			client := newResourceClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 || cycle {
					fmt.Fprintf(w, `{"networkVolumes":[%s],"pagination":{"nextCursor":"next","hasNextPage":true}}`, volumeRow)
				} else {
					fmt.Fprintf(w, `{"networkVolumes":[%s],"pagination":{"nextCursor":null,"hasNextPage":false}}`, volumeRow)
				}
			})
			if rows, err := client.ListNetworkVolumes(context.Background()); err == nil || rows != nil || calls != 2 {
				t.Fatalf("rows=%+v calls=%d err=%v", rows, calls, err)
			}
		})
	}
}
