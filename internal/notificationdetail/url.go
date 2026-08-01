package notificationdetail

import (
	"net/url"
	"strings"
)

// NotificationURL builds the private H5 route for one immutable notification
// snapshot. An empty result means callers must send the notification without a
// button instead of exposing a broken or unsafe link.
func NotificationURL(publicAppURL, notificationID string) string {
	notificationID = strings.TrimSpace(notificationID)
	if !notificationIDPattern.MatchString(notificationID) {
		return ""
	}
	target, err := url.Parse(strings.TrimSpace(publicAppURL))
	if err != nil || target.Host == "" || (target.Scheme != "https" && target.Scheme != "http") {
		return ""
	}
	query := target.Query()
	query.Set("notification_id", notificationID)
	target.RawQuery = query.Encode()
	target.Fragment = ""
	return target.String()
}
