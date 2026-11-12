package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEventRetainsZeroStockAndViewerCount(t *testing.T) {
	zero := int64(0)
	raw, err := json.Marshal(Event{ID: "event-1", Type: "SOLD_OUT", Stock: &zero, Count: &zero})
	if err != nil {
		t.Fatal(err)
	}
	payload := string(raw)
	if !strings.Contains(payload, `"stock":0`) || !strings.Contains(payload, `"count":0`) {
		t.Fatalf("zero-valued event fields were omitted: %s", payload)
	}
}
