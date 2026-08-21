package grid

import "testing"

func TestDecideGridFill(t *testing.T) {
	const q = 1.0

	t.Run("full fill applies engine", func(t *testing.T) {
		d := decideGridFill("filled", q, q, 0, q)
		if !d.applyEngine || !d.markProcessed || d.replaceQty != 0 {
			t.Fatalf("%+v", d)
		}
	})

	t.Run("partial tracks only", func(t *testing.T) {
		d := decideGridFill("partially_filled", q, 0.3, 0, q)
		if !d.trackOnly || d.applyEngine || d.markProcessed || d.newConsumed != 0.3 {
			t.Fatalf("%+v", d)
		}
	})

	t.Run("partial with no new qty is noop", func(t *testing.T) {
		d := decideGridFill("partially_filled", q, 0.3, 0.3, q)
		if d.trackOnly || d.applyEngine || d.markProcessed {
			t.Fatalf("%+v", d)
		}
	})

	t.Run("cancel with zero fill replaces full qty", func(t *testing.T) {
		d := decideGridFill("canceled", q, 0, 0, q)
		if d.applyEngine || !d.markProcessed || d.replaceQty != q {
			t.Fatalf("%+v", d)
		}
	})

	t.Run("cancel after partial replaces remainder", func(t *testing.T) {
		d := decideGridFill("canceled", q, 0.3, 0.3, q)
		if d.applyEngine || !d.markProcessed {
			t.Fatalf("%+v", d)
		}
		if diff := d.replaceQty - 0.7; diff > 1e-9 || diff < -1e-9 {
			t.Fatalf("replaceQty=%v want 0.7", d.replaceQty)
		}
	})

	t.Run("cancel with dust remainder treats as full fill", func(t *testing.T) {
		d := decideGridFill("canceled", q, 0.995, 0.995, q)
		if !d.applyEngine || !d.markProcessed || d.replaceQty != 0 {
			t.Fatalf("%+v", d)
		}
	})

	t.Run("new is noop", func(t *testing.T) {
		d := decideGridFill("new", q, 0, 0, q)
		if d.applyEngine || d.trackOnly || d.markProcessed || d.replaceQty != 0 {
			t.Fatalf("%+v", d)
		}
	})

	t.Run("replacement order full fill still applies engine", func(t *testing.T) {
		// 原单部成 0.3 后重挂 0.7，0.7 全成 → OnFill 一次（整格），与交易所累计 1.0 对齐。
		d := decideGridFill("filled", 0.7, 0.7, 0, q)
		if !d.applyEngine || !d.markProcessed {
			t.Fatalf("%+v", d)
		}
	})
}
