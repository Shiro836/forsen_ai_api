package donations

import (
	"fmt"
	"math"
	"strings"
)

// ISO 4217 currencies without two decimal places. The providers only send a
// handful of these, but the table is cheap and guessing 100 is wrong.
var currencyExponent = map[string]int{
	"BIF": 0, "CLP": 0, "DJF": 0, "GNF": 0, "ISK": 0, "JPY": 0, "KMF": 0,
	"KRW": 0, "PYG": 0, "RWF": 0, "UGX": 0, "UYI": 0, "VND": 0, "VUV": 0,
	"XAF": 0, "XOF": 0, "XPF": 0,
	"BHD": 3, "IQD": 3, "JOD": 3, "KWD": 3, "LYD": 3, "OMR": 3, "TND": 3,
}

func normalizeCurrency(currency string) string {
	return strings.ToUpper(strings.TrimSpace(currency))
}

func currencyExponentFor(currency string) int {
	if exp, ok := currencyExponent[currency]; ok {
		return exp
	}
	return 2
}

// MinorUnits converts a provider's major-unit float to minor units. Providers
// send floats, so this is the only place rounding happens.
func MinorUnits(amount float64, currency string) int64 {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
		return 0
	}
	scale := math.Pow10(currencyExponentFor(normalizeCurrency(currency)))
	return int64(math.Round(amount * scale))
}

// FormatAmount renders a minor-unit amount for the spoken line, keeping the
// currency's own precision.
func FormatAmount(amountMinor int64, currency string) string {
	if amountMinor <= 0 {
		return ""
	}
	currency = normalizeCurrency(currency)
	if currency == "" {
		return ""
	}
	exp := currencyExponentFor(currency)
	major := float64(amountMinor) / math.Pow10(exp)
	return fmt.Sprintf("%.*f %s", exp, major, currency)
}

// USDMinor converts a minor-unit amount at units-per-USD. ok is false when the
// currency has no rate; the caller stores no value rather than guessing one.
func USDMinor(amountMinor int64, currency string, rate float64) (usdMinor int64, ok bool) {
	if amountMinor <= 0 || rate <= 0 {
		return 0, false
	}
	major := float64(amountMinor) / math.Pow10(currencyExponentFor(normalizeCurrency(currency)))
	return int64(math.Round(major / rate * 100)), true
}
