package broker

import (
	"testing"

	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
)

func TestVenueFillUnchanged(t *testing.T) {
	o := &store.OrderRow{Status: "partially_filled", FilledQty: 0.3}
	if !venueFillUnchanged(o, "partially_filled", "0.3") {
		t.Fatal("same fill should be unchanged")
	}
	if venueFillUnchanged(o, "partially_filled", "0.5") {
		t.Fatal("increased fill should change")
	}
	if venueFillUnchanged(o, "filled", "0.3") {
		t.Fatal("status change should not be unchanged")
	}
}
