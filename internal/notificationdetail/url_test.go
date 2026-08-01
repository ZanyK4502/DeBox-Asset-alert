package notificationdetail

import "testing"

const testActionNotificationID = "nd_0123456789abcdef0123456789abcdef01234567"

func TestNotificationURLBuildsExactDetailRoute(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"plain base":       "https://alerts.example/app?notification_id=" + testActionNotificationID,
		"fragment removed": "https://alerts.example/root/?notification_id=" + testActionNotificationID,
		"query preserved":  "https://alerts.example/app?language=zh&notification_id=" + testActionNotificationID,
	}
	inputs := map[string]string{
		"plain base":       "https://alerts.example/app",
		"fragment removed": "https://alerts.example/root/#old",
		"query preserved":  "https://alerts.example/app?language=zh#old",
	}
	for name, want := range tests {
		name, want := name, want
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := NotificationURL(inputs[name], testActionNotificationID); got != want {
				t.Fatalf("NotificationURL() = %q, want %q", got, want)
			}
		})
	}
}

func TestNotificationURLRejectsBrokenButtons(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		base string
		id   string
	}{
		{"", testActionNotificationID},
		{"/relative", testActionNotificationID},
		{"javascript:alert(1)", testActionNotificationID},
		{"https://alerts.example", "nd_invalid"},
	} {
		if got := NotificationURL(test.base, test.id); got != "" {
			t.Fatalf("NotificationURL(%q, %q) = %q", test.base, test.id, got)
		}
	}
}
