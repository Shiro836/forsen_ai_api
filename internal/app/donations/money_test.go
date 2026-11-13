package donations

import "testing"

func TestMinorUnits(t *testing.T) {
	cases := []struct {
		amount   float64
		currency string
		want     int64
	}{
		{4.2, "USD", 420},
		{4.2, "rub", 420},
		{500, "JPY", 500},
		{1.234, "KWD", 1234},
		{0, "USD", 0},
		{-1, "USD", 0},
	}
	for _, tc := range cases {
		if got := MinorUnits(tc.amount, tc.currency); got != tc.want {
			t.Errorf("MinorUnits(%v, %q) = %d, want %d", tc.amount, tc.currency, got, tc.want)
		}
	}
}

func TestUSDMinor(t *testing.T) {
	if got, ok := USDMinor(420, "USD", 1); !ok || got != 420 {
		t.Fatalf("USD = %d, %v", got, ok)
	}
	if got, ok := USDMinor(420, "RUB", 82.8); !ok || got != 5 {
		t.Fatalf("RUB = %d, %v", got, ok)
	}
	if _, ok := USDMinor(420, "XYZ", 0); ok {
		t.Fatal("a missing rate must not produce a value")
	}
}
