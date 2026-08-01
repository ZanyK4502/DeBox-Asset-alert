package monitor

import (
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationfmt"
)

func plainNotificationText(text string) string {
	return strings.NewReplacer("<b>", "", "</b>", "").Replace(text)
}

func notificationBlockCount(text string) int {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	return strings.Count(text, notificationfmt.BlockBreak) + 1
}

func assertNotificationFieldFormatting(t *testing.T, text string) {
	t.Helper()
	if !strings.Contains(text, notificationfmt.BlockBreak) {
		t.Fatalf("notification fields are not separated: %s", text)
	}
	if strings.Contains(text, "</b>：") || strings.Contains(text, "</b>: ") {
		return
	}
	t.Fatalf("notification does not contain a bold key/value label: %s", text)
}
