package store

const subscriptionColumns = `
	id, debox_user_id, plan_code, status, starts_at, expires_at,
	daily_summary_enabled, daily_summary_time, daily_summary_timezone,
	daily_summary_chat_type, daily_summary_chat_id, daily_summary_label,
	daily_summary_language, daily_summary_last_sent_date,
	scheduled_push_last_sent_at, daily_summary_last_period_end_at, created_at
`

const watchRuleColumns = `
	id, debox_user_id, chain_key, chain_id, wallet_address, token_address,
	target_address, target_label, rule_type, threshold::text AS threshold,
	notification_chat_id, notification_chat_type, notification_label,
	notification_language, enabled, run_status, last_value, last_checked_at, created_at
`

const watchRuleColumnsQualified = `
	wr.id, wr.debox_user_id, wr.chain_key, wr.chain_id, wr.wallet_address, wr.token_address,
	wr.target_address, wr.target_label, wr.rule_type, wr.threshold::text AS threshold,
	wr.notification_chat_id, wr.notification_chat_type, wr.notification_label,
	wr.notification_language, wr.enabled, wr.run_status, wr.last_value, wr.last_checked_at, wr.created_at
`

const orderColumns = `
	id, debox_user_id, payer_address, plan_code, chain_key, chain_id,
	token_address, token_symbol, token_decimals, total_amount::text AS total_amount,
	recipient_address, tx_hash, status, created_at, expires_at, completed_at,
	tx_block_number, tx_confirmations, verified_at, verification_error
`

const alertEventColumns = `
	id, watch_rule_id, event_type, previous_value, current_value,
	notification_message_id, notification_status, notification_error,
	notification_attempts, notification_attempted_at, notification_sent_at, created_at
`

const alertEventColumnsQualified = `
	ae.id, ae.watch_rule_id, ae.event_type, ae.previous_value, ae.current_value,
	ae.notification_message_id, ae.notification_status, ae.notification_error,
	ae.notification_attempts, ae.notification_attempted_at, ae.notification_sent_at, ae.created_at
`

const notificationGroupColumns = `
	id, debox_user_id, gid, name, enabled, created_at
`

const userPreferenceColumns = `
	debox_user_id, free_watch_rule_id, bot_language, updated_at
`

const authChallengeColumns = `
	challenge_id, wallet_address, nonce_hash, message, expires_at, used_at, created_at
`

const authSessionColumns = `
	token_hash, debox_user_id, wallet_address, expires_at, revoked_at, last_seen_at, created_at
`

const complimentaryGrantColumns = `
	wallet_address, debox_user_id, plan_code, starts_at, expires_at, created_at
`
