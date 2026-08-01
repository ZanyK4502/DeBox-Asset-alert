package notificationfmt

import (
	"strings"
	"testing"
	"time"
)

func TestJoinLinesUsesDeBoxHTMLBreaksAndOmitsEmptyLines(t *testing.T) {
	text := JoinLines("<b>Alert</b>", "", "  ", "Value: 1", "Time: now")
	if text != "<b>Alert</b><br/>Value: 1<br/>Time: now" {
		t.Fatalf("JoinLines() = %q", text)
	}
	if strings.Contains(text, "\n") {
		t.Fatalf("JoinLines() contains a plain newline: %q", text)
	}
}

func TestJoinBlocksKeepsNotificationFieldsSeparated(t *testing.T) {
	text := JoinBlocks("<b>Alert</b>", "<b>Value</b>: 1", "<b>Time</b>: now")
	if text != "<b>Alert</b><br/><br/><b>Value</b>: 1<br/><br/><b>Time</b>: now" {
		t.Fatalf("JoinBlocks() = %q", text)
	}
}

func TestKeyValueUsesLanguageAppropriatePunctuation(t *testing.T) {
	if got := KeyValue("金额", "$1,250", false); got != "<b>金额</b>：$1,250" {
		t.Fatalf("Chinese KeyValue() = %q", got)
	}
	if got := KeyValue("Value", "$1,250", true); got != "<b>Value</b>: $1,250" {
		t.Fatalf("English KeyValue() = %q", got)
	}
	if got := KeyValue("<unsafe>", "safe", true); got != "<b>&lt;unsafe&gt;</b>: safe" {
		t.Fatalf("escaped KeyValue() = %q", got)
	}
	if got := KeyValue("Value", "", true); got != "" {
		t.Fatalf("empty KeyValue() = %q", got)
	}
}

func TestSharedNumberFormatting(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"money", Money("85240.75"), "85,240.75"},
		{"rounded money", Money("1999.999"), "2,000"},
		{"signed money", SignedMoney("850.5"), "+850.5"},
		{"compact money", CompactMoney("121080"), "121.1K"},
		{"token amount", TokenAmount("2850000"), "2.85M"},
		{"full token amount", FullTokenAmount("2850000.125"), "2,850,000.125"},
		{"small token amount", TokenAmount("0.00001234"), "0.00001234"},
		{"small price", Price("0.02984"), "0.02984"},
		{"tiny price", Price("0.00000042"), "0.00000042"},
		{"percentage", Percentage("12.345"), "12.35"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("got %q, want %q", test.got, test.want)
			}
		})
	}
}

func TestSharedIdentityFormatting(t *testing.T) {
	if got := ShortIdentifier("0x32b7123456789012345678901234567890122c69"); got != "0x32b7…2c69" {
		t.Fatalf("ShortIdentifier() = %q", got)
	}
	if got := ChainName("bsc"); got != "BNB Chain" {
		t.Fatalf("ChainName() = %q", got)
	}
}

func TestTimeFormattingShowsTimezoneOnce(t *testing.T) {
	start := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	if got := LocalTime(start, "Asia/Shanghai", "2006-01-02 15:04:05"); got != "2026-07-26 20:00:00" {
		t.Fatalf("LocalTime() = %q", got)
	}
	if got := TimeWindow(start, end, "UTC"); got != "2026-07-26 12:00 — 2026-07-26 13:00 (UTC)" {
		t.Fatalf("TimeWindow() = %q", got)
	}
}
