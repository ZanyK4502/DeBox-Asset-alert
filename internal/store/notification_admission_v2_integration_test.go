package store

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
)

func TestPostgresV2AdmissionSharesRollingHundredAcrossRestartsAndDoesNotRefundConsumed(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	for range 100 {
		permit, err := newWithDB(pool).ReserveNotificationAdmission(ctx, "v2-bot", 100)
		if err != nil {
			t.Fatal(err)
		}
		if err = db.CheckNotificationAdmission(ctx, permit); err != nil {
			t.Fatal(err)
		}
		if err = db.ReleaseNotificationAdmission(ctx, permit); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := newWithDB(pool).ReserveNotificationAdmission(ctx, "v2-bot", 100); err == nil {
		t.Fatal("new instance bypassed rolling 100")
	}
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT reservations FROM notification_send_admission WHERE bot_id='v2-bot'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var permits []notificationdelivery.AdmissionPermit
	if json.Unmarshal(raw, &permits) != nil || len(permits) != 100 {
		t.Fatalf("persisted slots=%d", len(permits))
	}
	for _, permit := range permits {
		if !permit.Consumed {
			t.Fatal("consumed failure slot refunded")
		}
	}
}

func TestPostgresV2PathAdmissionIsSharedAcrossInstancesAndBotsAndBounded(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	scope := strings.Repeat("a", 64)
	var permits []notificationdelivery.AdmissionPermit
	for i := 0; i < 32; i++ {
		bot := "first-bot"
		if i%2 == 1 {
			bot = "second-bot"
		}
		p, err := db.ReserveNotificationAdmission(ctx, bot, 100)
		if err != nil {
			t.Fatal(err)
		}
		permits = append(permits, p)
	}
	var group sync.WaitGroup
	errors := make(chan error, len(permits))
	for _, permit := range permits {
		group.Add(1)
		go func(p notificationdelivery.AdmissionPermit) {
			defer group.Done()
			err := newWithDB(pool).CheckNotificationHTTPAdmission(ctx, p, scope)
			if err != nil {
				if _, waiting := notificationdelivery.AdmissionRetryAt(err); !waiting {
					errors <- err
				}
			}
		}(permit)
	}
	group.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	rows, err := pool.Query(ctx, `SELECT reservations FROM notification_send_admission ORDER BY bot_id`)
	if err != nil {
		t.Fatal(err)
	}
	var admitted []time.Time
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var saved []notificationdelivery.AdmissionPermit
		if err := json.Unmarshal(raw, &saved); err != nil {
			t.Fatal(err)
		}
		for _, p := range saved {
			if p.Consumed {
				admitted = append(admitted, p.GrantedAt)
			}
		}
	}
	rows.Close()
	if len(admitted) < 10 {
		t.Fatalf("unexpectedly admitted only %d", len(admitted))
	}
	sort.Slice(admitted, func(i, j int) bool { return admitted[i].Before(admitted[j]) })
	for i, at := range admitted {
		n := 0
		for _, previous := range admitted[:i+1] {
			if previous.Add(time.Second).After(at) {
				n++
			}
		}
		if n > 10 {
			t.Fatalf("shared scope admitted %d in rolling second", n)
		}
	}
	var slots int
	if err := pool.QueryRow(ctx, `SELECT jsonb_array_length(admissions) FROM notification_send_path_admission WHERE scope_hash=$1`, scope).Scan(&slots); err != nil || slots > 10 {
		t.Fatalf("path storage=%d err=%v", slots, err)
	}
	// A different key/path scope has its own second budget while still using
	// the same bot's shared minute reservations.
	p, err := db.ReserveNotificationAdmission(ctx, "first-bot", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CheckNotificationHTTPAdmission(ctx, p, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if err = newWithDB(pool).CheckNotificationHTTPAdmission(ctx, p, strings.Repeat("c", 64)); err == nil {
		t.Fatal("copied permit reused under another path")
	}
}

func TestPostgresV2AdmissionPathWriteFailureRollsBackMinuteConsumption(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	scope := strings.Repeat("a", 64)
	if _, err := pool.Exec(ctx, `INSERT INTO notification_send_path_admission(scope_hash) VALUES($1)`, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_v2_path_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected path failure'; END $$; CREATE TRIGGER reject_v2_path_write BEFORE UPDATE ON notification_send_path_admission FOR EACH ROW EXECUTE FUNCTION reject_v2_path_write()`); err != nil {
		t.Fatal(err)
	}
	permit, err := db.ReserveNotificationAdmission(ctx, "bot", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CheckNotificationHTTPAdmission(ctx, permit, scope); err == nil {
		t.Fatal("path write failure admitted HTTP")
	}
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT reservations FROM notification_send_admission WHERE bot_id='bot'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved []notificationdelivery.AdmissionPermit
	if json.Unmarshal(raw, &saved) != nil || len(saved) != 1 || saved[0].Consumed {
		t.Fatal("path failure committed minute consumption")
	}
	if err = db.ReleaseNotificationAdmission(ctx, permit); err != nil {
		t.Fatal(err)
	}
	var length int
	if err := pool.QueryRow(ctx, `SELECT jsonb_array_length(reservations) FROM notification_send_admission WHERE bot_id='bot'`).Scan(&length); err != nil || length != 0 {
		t.Fatalf("unconsumed permit not released=%d %v", length, err)
	}
}

func TestPostgresV2SecondAdmissionExpiryAndMalformedStateFailClosed(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	scope := strings.Repeat("d", 64)
	if _, err := pool.Exec(ctx, `INSERT INTO notification_send_path_admission(scope_hash,admissions) VALUES($1,(SELECT jsonb_agg(clock_timestamp()-INTERVAL '2 seconds') FROM generate_series(1,10)))`, scope); err != nil {
		t.Fatal(err)
	}
	p, err := db.ReserveNotificationAdmission(ctx, "bot", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CheckNotificationHTTPAdmission(ctx, p, scope); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = pool.QueryRow(ctx, `SELECT jsonb_array_length(admissions) FROM notification_send_path_admission WHERE scope_hash=$1`, scope).Scan(&n); err != nil || n != 1 {
		t.Fatalf("expired slot storage=%d %v", n, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE notification_send_path_admission SET admissions='["not-a-time"]'::jsonb WHERE scope_hash=$1`, scope); err != nil {
		t.Fatal(err)
	}
	p, err = db.ReserveNotificationAdmission(ctx, "bot", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CheckNotificationHTTPAdmission(ctx, p, scope); err == nil {
		t.Fatal("malformed quota state admitted HTTP")
	}
}
