package marketrules

import (
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationfmt"
)

func plainMarketNotificationText(text string) string {
	return strings.NewReplacer("<b>", "", "</b>", "").Replace(text)
}

func marketNotificationBlockCount(text string) int {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	return strings.Count(text, notificationfmt.BlockBreak) + 1
}
