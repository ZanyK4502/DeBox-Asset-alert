package store

import (
	"context"
	"fmt"
)

const summaryLockNamespace int32 = 7_220_026

func (s *Store) CreateNotificationGroup(
	ctx context.Context,
	deboxUserID string,
	gid string,
	name string,
) (NotificationGroup, error) {
	return createNotificationGroup(ctx, s.db, deboxUserID, gid, name)
}

func createNotificationGroup(
	ctx context.Context,
	db DBTX,
	deboxUserID string,
	gid string,
	name string,
) (NotificationGroup, error) {
	group, err := collectOne[NotificationGroup](ctx, db, `
		INSERT INTO notification_groups (debox_user_id, gid, name, enabled)
		VALUES ($1, $2, $3, 1)
		ON CONFLICT (debox_user_id, gid)
		DO UPDATE SET name = EXCLUDED.name, enabled = 1
		RETURNING `+notificationGroupColumns,
		deboxUserID,
		gid,
		name,
	)
	if err != nil {
		return NotificationGroup{}, fmt.Errorf("create notification group: %w", err)
	}
	return group, nil
}

func (s *Store) GetNotificationGroup(
	ctx context.Context,
	deboxUserID string,
	gid string,
) (*NotificationGroup, error) {
	group, err := collectOptional[NotificationGroup](ctx, s.db, `
		SELECT `+notificationGroupColumns+`
		FROM notification_groups
		WHERE debox_user_id = $1 AND gid = $2 AND enabled = 1
		LIMIT 1
	`, deboxUserID, gid)
	if err != nil {
		return nil, fmt.Errorf("get notification group: %w", err)
	}
	return group, nil
}

func (s *Store) ListNotificationGroups(
	ctx context.Context,
	deboxUserID string,
) ([]NotificationGroup, error) {
	groups, err := collectMany[NotificationGroup](ctx, s.db, `
		SELECT `+notificationGroupColumns+`
		FROM notification_groups
		WHERE debox_user_id = $1 AND enabled = 1
		ORDER BY created_at DESC
	`, deboxUserID)
	if err != nil {
		return nil, fmt.Errorf("list notification groups: %w", err)
	}
	return groups, nil
}

func (s *Store) CountNotificationGroups(ctx context.Context, deboxUserID string) (int64, error) {
	count, err := queryCount(ctx, s.db, `
		SELECT COUNT(*)
		FROM notification_groups
		WHERE debox_user_id = $1 AND enabled = 1
	`, deboxUserID)
	if err != nil {
		return 0, fmt.Errorf("count notification groups: %w", err)
	}
	return count, nil
}

type GroupDeletion struct {
	Group                NotificationGroup `json:"group"`
	SummaryTargetChanged bool              `json:"summary_target_changed"`
	SummaryDisabled      bool              `json:"summary_disabled"`
}

func (s *Store) DeleteNotificationGroup(
	ctx context.Context,
	groupID int64,
	deboxUserID string,
) (GroupDeletion, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (GroupDeletion, error) {
		group, err := collectOne[NotificationGroup](ctx, tx, `
			SELECT `+notificationGroupColumns+`
			FROM notification_groups
			WHERE id = $1 AND debox_user_id = $2
			FOR UPDATE
		`, groupID, deboxUserID)
		if isNoRows(err) {
			return GroupDeletion{}, ErrNotFound
		}
		if err != nil {
			return GroupDeletion{}, fmt.Errorf("lock notification group: %w", err)
		}

		rows, err := tx.Query(ctx, `
			SELECT DISTINCT s.id
			FROM subscriptions s
			JOIN daily_summary_targets t ON t.subscription_id = s.id
			WHERE s.debox_user_id = $1
			  AND s.status = 'active'
			  AND (s.is_permanent = 1 OR s.expires_at > NOW())
			  AND t.chat_type = 'group'
			  AND t.chat_id = $2
			ORDER BY s.id ASC
		`, deboxUserID, group.GID)
		if err != nil {
			return GroupDeletion{}, fmt.Errorf("list group summary subscriptions: %w", err)
		}
		subscriptionIDs, err := collectInt64Rows(rows)
		if err != nil {
			return GroupDeletion{}, fmt.Errorf("read group summary subscriptions: %w", err)
		}
		for _, subscriptionID := range subscriptionIDs {
			if _, err := tx.Exec(
				ctx,
				"SELECT pg_advisory_xact_lock($1, $2)",
				summaryLockNamespace,
				int32(subscriptionID),
			); err != nil {
				return GroupDeletion{}, fmt.Errorf("lock scheduled summary %d: %w", subscriptionID, err)
			}
		}

		if _, err := tx.Exec(ctx, `
			UPDATE notification_groups
			SET enabled = 0
			WHERE id = $1 AND debox_user_id = $2
		`, groupID, deboxUserID); err != nil {
			return GroupDeletion{}, fmt.Errorf("disable notification group: %w", err)
		}

		if _, err := tx.Exec(ctx, `
			UPDATE watch_rules
			SET notification_chat_id = $1,
			    notification_chat_type = 'private',
			    notification_label = '私聊通知'
			WHERE debox_user_id = $1
			  AND notification_chat_type = 'group'
			  AND notification_chat_id = $2
		`, deboxUserID, group.GID); err != nil {
			return GroupDeletion{}, fmt.Errorf("fallback group watch rules to private: %w", err)
		}

		if _, err := tx.Exec(ctx, `
			UPDATE combination_rules
			SET notification_chat_id = $1,
			    notification_chat_type = 'private',
			    notification_label = '私聊通知'
			WHERE debox_user_id = $1
			  AND notification_chat_type = 'group'
			  AND notification_chat_id = $2
		`, deboxUserID, group.GID); err != nil {
			return GroupDeletion{}, fmt.Errorf("fallback group combination rules to private: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE market_rules
			SET notification_chat_id = $1,
			    notification_chat_type = 'private',
			    notification_label = '私聊通知',
			    updated_at = NOW()
			WHERE debox_user_id = $1
			  AND notification_chat_type = 'group'
			  AND notification_chat_id = $2
		`, deboxUserID, group.GID); err != nil {
			return GroupDeletion{}, fmt.Errorf("fallback group market rules to private: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE market_combination_rules
			SET notification_chat_id = $1,
			    notification_chat_type = 'private',
			    notification_label = '私聊通知',
			    updated_at = NOW()
			WHERE debox_user_id = $1
			  AND notification_chat_type = 'group'
			  AND notification_chat_id = $2
		`, deboxUserID, group.GID); err != nil {
			return GroupDeletion{}, fmt.Errorf(
				"fallback group market combinations to private: %w",
				err,
			)
		}

		if _, err := tx.Exec(ctx, `
			DELETE FROM daily_summary_targets t
			USING subscriptions s
			WHERE t.subscription_id = s.id
			  AND s.debox_user_id = $1
			  AND t.chat_type = 'group'
			  AND t.chat_id = $2
		`, deboxUserID, group.GID); err != nil {
			return GroupDeletion{}, fmt.Errorf("remove group from daily summary targets: %w", err)
		}
		var disabledCount int64
		if len(subscriptionIDs) > 0 {
			if _, err := tx.Exec(ctx, `
				UPDATE subscriptions s
				SET daily_summary_chat_type = COALESCE((
					    SELECT t.chat_type
					    FROM daily_summary_targets t
					    WHERE t.subscription_id = s.id
					    ORDER BY CASE WHEN t.chat_type = 'private' THEN 0 ELSE 1 END, t.id ASC
					    LIMIT 1
				    ), 'private'),
				    daily_summary_chat_id = COALESCE((
					    SELECT t.chat_id
					    FROM daily_summary_targets t
					    WHERE t.subscription_id = s.id
					    ORDER BY CASE WHEN t.chat_type = 'private' THEN 0 ELSE 1 END, t.id ASC
					    LIMIT 1
				    ), s.debox_user_id)
				WHERE s.id = ANY($1::bigint[])
			`, subscriptionIDs); err != nil {
				return GroupDeletion{}, fmt.Errorf("refresh daily summary compatibility target: %w", err)
			}
			tag, err := tx.Exec(ctx, `
				UPDATE subscriptions s
				SET daily_summary_enabled = 0
				WHERE s.id = ANY($1::bigint[])
				  AND s.daily_summary_enabled = 1
				  AND NOT EXISTS (
					  SELECT 1
					  FROM daily_summary_targets t
					  WHERE t.subscription_id = s.id
				  )
			`, subscriptionIDs)
			if err != nil {
				return GroupDeletion{}, fmt.Errorf("disable daily summaries without targets: %w", err)
			}
			disabledCount = tag.RowsAffected()
		}
		return GroupDeletion{
			Group:                group,
			SummaryTargetChanged: len(subscriptionIDs) > 0,
			SummaryDisabled:      disabledCount > 0,
		}, nil
	})
}

func (s *Store) DisableDailySummaries(
	ctx context.Context,
	subscriptionIDs []int64,
	deboxUserID string,
) (int64, error) {
	if len(subscriptionIDs) == 0 {
		return 0, nil
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE subscriptions
		SET daily_summary_enabled = 0
		WHERE debox_user_id = $1 AND id = ANY($2::bigint[])
	`, deboxUserID, subscriptionIDs)
	if err != nil {
		return 0, fmt.Errorf("disable daily summaries: %w", err)
	}
	return tag.RowsAffected(), nil
}
