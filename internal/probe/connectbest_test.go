package probe

import (
	"testing"

	"github.com/flashproxy/flashproxy-status/internal/model"
)

func TestBetterConnect(t *testing.T) {
	ok := func(ttfb, conn uint32) model.ProbeResult {
		return model.ProbeResult{Success: 1, TTFBMS: ttfb, ConnectMS: conn}
	}
	fail := func(e string) model.ProbeResult { return model.ProbeResult{Success: 0, ErrorType: e} }

	// success beats failure regardless of order
	if betterConnect(fail("x"), ok(99, 99)).Success != 1 {
		t.Fatal("success must beat failure (b)")
	}
	if betterConnect(ok(99, 99), fail("x")).Success != 1 {
		t.Fatal("success must beat failure (a)")
	}
	// lower ttfb wins between successes
	if got := betterConnect(ok(50, 10), ok(20, 99)); got.TTFBMS != 20 {
		t.Fatalf("lower ttfb must win, got %d", got.TTFBMS)
	}
	// tie on ttfb -> lower connect_ms wins
	if got := betterConnect(ok(20, 30), ok(20, 5)); got.ConnectMS != 5 {
		t.Fatalf("tie ttfb -> lower connect wins, got %d", got.ConnectMS)
	}
	// two failures -> keep first (still Down)
	if got := betterConnect(fail("a"), fail("b")); got.Success != 0 || got.ErrorType != "a" {
		t.Fatalf("two failures keep first, got %+v", got)
	}
}
