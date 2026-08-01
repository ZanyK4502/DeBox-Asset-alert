package notificationfmt

import (
	"html"
	"math/big"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

const (
	LineBreak       = "<br/>"
	BlockBreak      = "<br/><br/>"
	defaultTimezone = "Asia/Shanghai"
)

// JoinLines joins notification lines with the HTML line break understood by DeBox.
// Empty lines are omitted so missing fields do not leave visual gaps.
func JoinLines(lines ...string) string {
	visible := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			visible = append(visible, line)
		}
	}
	return strings.Join(visible, LineBreak)
}

// JoinBlocks keeps notification fields visually separated on narrow DeBox
// clients. It is intended for top-level notification sections and key/value
// fields; compact lists and timelines should continue to use JoinLines.
func JoinBlocks(blocks ...string) string {
	visible := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if strings.TrimSpace(block) != "" {
			visible = append(visible, block)
		}
	}
	return strings.Join(visible, BlockBreak)
}

func Separator(english bool) string {
	if english {
		return ": "
	}
	return "："
}

func KeyValue(label, value string, english bool) string {
	label = strings.TrimSpace(label)
	value = strings.TrimSpace(value)
	if label == "" || value == "" {
		return ""
	}
	return "<b>" + html.EscapeString(label) + "</b>" + Separator(english) + value
}

func Label(label string, english bool) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	return "<b>" + html.EscapeString(label) + "</b>" + strings.TrimSpace(Separator(english))
}

func ChainName(chainKey string) string {
	chainKey = strings.TrimSpace(chainKey)
	if chainKey == "" {
		return ""
	}
	profile, err := chain.ChainProfile(chainKey, "")
	if err == nil {
		return profile.Name
	}
	return chainKey
}

func ShortIdentifier(value string) string {
	value = strings.TrimSpace(value)
	characters := []rune(value)
	if len(characters) <= 16 {
		return value
	}
	return string(characters[:6]) + "…" + string(characters[len(characters)-4:])
}

// Money formats a full monetary amount with thousands separators and at most
// two decimal places. It intentionally does not include a currency symbol.
func Money(value string) string {
	number, ok := parseNumber(value)
	if !ok {
		return strings.TrimSpace(value)
	}
	return formatNumber(number, 2, true)
}

func SignedMoney(value string) string {
	number, ok := parseNumber(value)
	if !ok {
		return strings.TrimSpace(value)
	}
	formatted := formatNumber(number, 2, true)
	if number.Sign() > 0 {
		return "+" + formatted
	}
	return formatted
}

// CompactMoney keeps large headline amounts easy to scan, for example
// 121080 -> 121.1K. It intentionally does not include a currency symbol.
func CompactMoney(value string) string {
	return compactNumber(value, 2)
}

// TokenAmount preserves useful precision for small values and abbreviates
// values of one thousand or more.
func TokenAmount(value string) string {
	number, ok := parseNumber(value)
	if !ok {
		return strings.TrimSpace(value)
	}
	absolute := new(big.Rat).Abs(new(big.Rat).Set(number))
	if absolute.Cmp(big.NewRat(1000, 1)) >= 0 {
		return compactRat(number, 2)
	}
	return formatNumber(number, precisionForMagnitude(absolute), true)
}

// FullTokenAmount keeps the full, grouped quantity for balances, thresholds,
// and allowances where an abbreviated value would hide useful detail.
func FullTokenAmount(value string) string {
	number, ok := parseNumber(value)
	if !ok {
		return strings.TrimSpace(value)
	}
	absolute := new(big.Rat).Abs(new(big.Rat).Set(number))
	precision := precisionForMagnitude(absolute)
	if absolute.Cmp(big.NewRat(1, 1)) >= 0 {
		precision = 4
	}
	return formatNumber(number, precision, true)
}

// Price preserves more decimals as a price approaches zero, preventing small
// but meaningful prices from being rounded to $0.00.
func Price(value string) string {
	number, ok := parseNumber(value)
	if !ok {
		return strings.TrimSpace(value)
	}
	absolute := new(big.Rat).Abs(new(big.Rat).Set(number))
	return formatNumber(number, precisionForMagnitude(absolute), true)
}

func Percentage(value string) string {
	number, ok := parseNumber(value)
	if !ok {
		return strings.TrimSpace(value)
	}
	return formatNumber(number, 2, true)
}

func LocalTime(value time.Time, timezone, layout string) string {
	if value.IsZero() {
		return ""
	}
	location, _ := loadLocation(timezone)
	return value.In(location).Format(layout)
}

// TimeWindow renders a local start/end range and includes the timezone once.
func TimeWindow(start, end time.Time, timezone string) string {
	location, timezone := loadLocation(timezone)
	const layout = "2006-01-02 15:04"
	switch {
	case start.IsZero() && end.IsZero():
		return ""
	case start.IsZero():
		return end.In(location).Format(layout) + " (" + timezone + ")"
	case end.IsZero():
		return start.In(location).Format(layout) + " (" + timezone + ")"
	default:
		return start.In(location).Format(layout) + " — " +
			end.In(location).Format(layout) + " (" + timezone + ")"
	}
}

func parseNumber(value string) (*big.Rat, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, false
	}
	number, ok := new(big.Rat).SetString(value)
	return number, ok
}

func compactNumber(value string, fallbackPrecision int) string {
	number, ok := parseNumber(value)
	if !ok {
		return strings.TrimSpace(value)
	}
	return compactRat(number, fallbackPrecision)
}

func compactRat(number *big.Rat, fallbackPrecision int) string {
	absolute := new(big.Rat).Abs(new(big.Rat).Set(number))
	units := []struct {
		minimum *big.Rat
		suffix  string
	}{
		{big.NewRat(1_000_000_000_000, 1), "T"},
		{big.NewRat(1_000_000_000, 1), "B"},
		{big.NewRat(1_000_000, 1), "M"},
		{big.NewRat(1_000, 1), "K"},
	}
	for _, unit := range units {
		if absolute.Cmp(unit.minimum) < 0 {
			continue
		}
		scaled := new(big.Rat).Quo(new(big.Rat).Set(number), unit.minimum)
		scaledAbsolute := new(big.Rat).Abs(new(big.Rat).Set(scaled))
		precision := fallbackPrecision
		if scaledAbsolute.Cmp(big.NewRat(10, 1)) >= 0 {
			precision = 1
		}
		return formatNumber(scaled, precision, false) + unit.suffix
	}
	return formatNumber(number, fallbackPrecision, true)
}

func precisionForMagnitude(absolute *big.Rat) int {
	switch {
	case absolute.Sign() == 0:
		return 2
	case absolute.Cmp(big.NewRat(1000, 1)) >= 0:
		return 2
	case absolute.Cmp(big.NewRat(1, 1)) >= 0:
		return 4
	case absolute.Cmp(big.NewRat(1, 100)) >= 0:
		return 6
	case absolute.Cmp(big.NewRat(1, 10_000)) >= 0:
		return 8
	default:
		return 12
	}
}

func formatNumber(number *big.Rat, precision int, grouping bool) string {
	text := number.FloatString(precision)
	if strings.Contains(text, ".") {
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	if text == "-0" {
		text = "0"
	}
	if grouping {
		text = groupThousands(text)
	}
	return text
}

func groupThousands(value string) string {
	sign := ""
	if strings.HasPrefix(value, "-") || strings.HasPrefix(value, "+") {
		sign, value = value[:1], value[1:]
	}
	parts := strings.SplitN(value, ".", 2)
	integer := parts[0]
	if len(integer) > 3 {
		first := len(integer) % 3
		if first == 0 {
			first = 3
		}
		var grouped strings.Builder
		grouped.WriteString(integer[:first])
		for index := first; index < len(integer); index += 3 {
			grouped.WriteByte(',')
			grouped.WriteString(integer[index : index+3])
		}
		integer = grouped.String()
	}
	if len(parts) == 2 {
		return sign + integer + "." + parts[1]
	}
	return sign + integer
}

func loadLocation(timezone string) (*time.Location, string) {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		timezone = defaultTimezone
	}
	location, err := time.LoadLocation(timezone)
	if err == nil {
		return location, timezone
	}
	location, _ = time.LoadLocation(defaultTimezone)
	return location, defaultTimezone
}
