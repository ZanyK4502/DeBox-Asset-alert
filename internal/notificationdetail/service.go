package notificationdetail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

var (
	ErrInvalidNotificationID = errors.New("通知 ID 格式不正确。")
	ErrNotificationNotFound  = errors.New("未找到这条通知详情。")
	ErrNotificationExpired   = errors.New("通知详情已过期。")

	notificationIDPattern = regexp.MustCompile(`^nd_[a-f0-9]{40}$`)
)

type Repository interface {
	GetNotificationDetailSnapshot(context.Context, string) (*store.NotificationDetailSnapshot, error)
}

type Settings struct {
	PublicAppURL string
	Now          func() time.Time
}

type Service struct {
	repository   Repository
	publicAppURL string
	now          func() time.Time
}

func New(repository Repository, settings Settings) *Service {
	if settings.Now == nil {
		settings.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		repository:   repository,
		publicAppURL: strings.TrimSpace(settings.PublicAppURL),
		now:          settings.Now,
	}
}

func (s *Service) Detail(
	ctx context.Context,
	deboxUserID string,
	notificationID string,
) (Detail, error) {
	deboxUserID = strings.TrimSpace(deboxUserID)
	notificationID = strings.TrimSpace(notificationID)
	if deboxUserID == "" || !notificationIDPattern.MatchString(notificationID) {
		return Detail{}, ErrInvalidNotificationID
	}
	if s == nil || s.repository == nil {
		return Detail{}, errors.New("notification detail repository is not configured")
	}
	snapshot, err := s.repository.GetNotificationDetailSnapshot(ctx, notificationID)
	if err != nil {
		if errors.Is(err, store.ErrInvalidNotificationSnapshot) {
			return Detail{}, ErrInvalidNotificationID
		}
		return Detail{}, fmt.Errorf("load notification detail: %w", err)
	}
	if snapshot == nil {
		return Detail{}, ErrNotificationNotFound
	}
	scope := normalizeScope(snapshot.NotificationChatType)
	if scope == "private" && snapshot.DeBoxUserID != deboxUserID {
		return Detail{}, ErrNotificationNotFound
	}
	if !s.now().Before(snapshot.ExpiresAt) {
		return Detail{}, ErrNotificationExpired
	}
	domain, deliveryMode, err := notificationClassification(snapshot.NotificationKind)
	if err != nil {
		return Detail{}, err
	}
	data, object, err := notificationData(snapshot.Details, scope == "group")
	if err != nil {
		return Detail{}, fmt.Errorf("prepare notification detail: %w", err)
	}
	copyValues, links := detailValuesAndLinks(
		object,
		normalizeLanguage(snapshot.NotificationLanguage),
		scope == "group",
	)
	ruleID := snapshot.RuleID
	if scope == "group" {
		ruleID = nil
	} else if link := managementLink(
		s.publicAppURL,
		snapshot.NotificationKind,
		snapshot.RuleID,
		object,
		normalizeLanguage(snapshot.NotificationLanguage),
	); link != nil {
		links = append(links, *link)
	}
	return Detail{
		SchemaVersion:    1,
		NotificationID:   snapshot.PublicID,
		NotificationKind: snapshot.NotificationKind,
		Domain:           domain,
		DeliveryMode:     deliveryMode,
		AccessScope:      scope,
		Language:         normalizeLanguage(snapshot.NotificationLanguage),
		Label:            snapshot.NotificationLabel,
		Rule: Rule{
			ID:        ruleID,
			Type:      snapshot.RuleType,
			Name:      snapshot.RuleName,
			Threshold: snapshot.RuleThreshold,
		},
		Target: Target{
			ChatType: scope,
			Label:    snapshot.NotificationLabel,
		},
		ActualValue:      snapshot.ActualValue,
		NotificationText: snapshot.NotificationText,
		Data:             data,
		CopyValues:       copyValues,
		Links:            links,
		CreatedAt:        snapshot.CreatedAt,
		ExpiresAt:        snapshot.ExpiresAt,
	}, nil
}

func notificationData(
	details json.RawMessage,
	group bool,
) (json.RawMessage, map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(details))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		if err == nil {
			err = errors.New("notification detail data must be an object")
		}
		return nil, nil, err
	}
	cleaned, _ := sanitizeDetailValue(object, group).(map[string]any)
	encoded, err := json.Marshal(cleaned)
	if err != nil {
		return nil, nil, err
	}
	return encoded, cleaned, nil
}

func notificationClassification(kind string) (string, string, error) {
	switch kind {
	case store.NotificationKindAddressRealtime:
		return "address", "realtime", nil
	case store.NotificationKindAddressStage:
		return "address", "stage", nil
	case store.NotificationKindAddressCombination:
		return "address", "combination", nil
	case store.NotificationKindMarketRealtime:
		return "market", "realtime", nil
	case store.NotificationKindMarketStage:
		return "market", "stage", nil
	case store.NotificationKindMarketCombination:
		return "market", "combination", nil
	case store.NotificationKindDailySummary:
		return "daily_summary", "summary", nil
	default:
		return "", "", fmt.Errorf("unsupported notification detail kind %q", kind)
	}
}

func normalizeScope(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "group") {
		return "group"
	}
	return "private"
}

func normalizeLanguage(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "en") {
		return "en"
	}
	return "zh"
}
