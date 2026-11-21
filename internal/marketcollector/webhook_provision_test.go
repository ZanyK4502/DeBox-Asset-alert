package marketcollector

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

// Exercises the actual client contract and PostgreSQL registry/inbox. The
// provider is loopback only and obscures its signing key on every GET.
func TestManagedTransferLifecycle(t *testing.T) {
	ctx := context.Background()
	db, pool := scannerPerformanceStore(t)
	const owner = "local-hook-owner"
	if _, err := db.ActivateSubscription(ctx, owner, plans.Professional, 30); err != nil {
		t.Fatal(err)
	}
	foreign := chain.WebhookSubscription{SubscriptionID: "82", Description: "another app", Notification: chain.WebhookNotification{WebhookURL: "https://unrelated.example/callback"}, EventType: "TOKEN_TRANSFER", IsActive: true, IsInstant: true}
	var registered *chain.WebhookSubscription
	posts, patches := 0, 0
	const secret = "local-hmac-signing-key"
	statusToReturn := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/base/mainnet/webhooks") || r.Header.Get("X-API-KEY") != "fixture-project-key" {
			t.Errorf("wrong Nodit API target")
			w.WriteHeader(400)
			return
		}
		var result any
		switch r.Method {
		case http.MethodGet:
			items := []chain.WebhookSubscription{foreign}
			if registered != nil {
				item := *registered
				item.SigningKey = ""
				items = append(items, item)
			}
			result = chain.WebhookList{Total: int64(len(items)), Page: 1, RPP: 100, Items: items}
		case http.MethodPost:
			posts++
			if registered != nil {
				t.Errorf("duplicate POST")
				w.WriteHeader(409)
				return
			}
			var input chain.WebhookCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if statusToReturn == 429 {
				w.WriteHeader(429)
				return
			}
			cond, _ := json.Marshal(input.Condition)
			registered = &chain.WebhookSubscription{SubscriptionID: "123", Description: input.Description, EventType: input.EventType, Notification: input.Notification, Condition: cond, IsInstant: input.IsInstant, IsActive: true, SigningKey: secret}
			if statusToReturn == 503 {
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(201)
			result = map[string]string{"subscriptionId": registered.SubscriptionID, "signingKey": secret}
		case http.MethodPatch:
			if r.URL.Path != "/base/mainnet/webhooks/123" || registered == nil {
				t.Errorf("PATCH to wrong subscription")
				w.WriteHeader(400)
				return
			}
			patches++
			var input chain.WebhookUpdateRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
				return
			}
			if input.IsActive != nil {
				registered.IsActive = *input.IsActive
			}
			if input.Condition != nil {
				registered.Condition, _ = json.Marshal(input.Condition)
			}
			if input.Notification != nil {
				registered.Notification = *input.Notification
			}
			result = map[string]bool{"result": true}
		default:
			t.Errorf("unexpected verb")
			w.WriteHeader(400)
			return
		}
		if err := json.NewEncoder(w).Encode(result); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client, err := chain.NewClient("fixture-project-key", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	deps := Dependencies{Repository: db, Chain: client}
	cfg := Settings{Enabled: true, ChainKey: "base", ChainID: 8453, PublicAppURL: "https://bot.example", WebhookAutoRepair: true, WebhookWrappingKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'t'}, 32))}
	service := New(deps, cfg)
	sync := func() {
		t.Helper()
		if err := service.SyncWebhookSubscriptions(ctx); err != nil {
			t.Fatal(err)
		}
	}
	sync()
	if posts != 0 {
		t.Fatal("created webhook on idle chain")
	}
	policy := store.QuotaPolicy{PlanCode: plans.Professional, MarketProjectLimit: 5, MultiPoolMonitoring: true}
	createProject := func(token string) {
		t.Helper()
		address := "0xaa" + token[4:]
		pool, err := db.UpsertMarketPool(ctx, store.UpsertMarketPoolParams{ChainKey: "base", ChainID: 8453, Protocol: "uniswap", ProtocolVersion: "v2", PoolKey: address, PoolAddress: &address, Token0Address: token, Token1Address: "0xcccccccccccccccccccccccccccccccccccccccc", Token0DecimalsVerified: true, Token1DecimalsVerified: true, SupportsEventParsing: true, ParserAdapter: "evm_v2", VerificationStatus: "verified"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.CreateMultiChainMarketProjectWithinQuota(ctx, store.CreateMultiChainMarketProjectParams{DeBoxUserID: owner, CanonicalName: "Hook sample", Symbol: "H", IdentitySource: "local_fixture", CanonicalAssetID: token, VerificationStatus: "single_chain", Deployments: []store.CreateMultiChainMarketDeploymentParams{{ChainKey: "base", ChainID: 8453, TokenAddress: token, TokenSymbol: "H", VerificationStatus: "verified", VerificationSource: "fixture", Pools: []store.CreateMultiChainMarketPoolParams{{MarketPoolID: pool.ID, Selected: true, IsPrimary: true}}}}}, policy)
		if err != nil {
			t.Fatal(err)
		}
	}
	createProject("0x1111111111111111111111111111111111111111")
	withoutKey := cfg
	withoutKey.WebhookWrappingKey = ""
	if err := New(deps, withoutKey).SyncWebhookSubscriptions(ctx); !errors.Is(err, ErrWebhookUnavailable) || posts != 0 {
		t.Fatal("create proceeded without a durable signing key destination", err)
	}
	statusToReturn = 429
	if err := service.SyncWebhookSubscriptions(ctx); err == nil {
		t.Fatal("provider rejection accepted")
	}
	statusToReturn = 0
	sync()
	service = New(deps, cfg) // restart before receiving the first delivery
	sync()
	if posts != 2 || patches != 0 {
		t.Fatalf("unnecessary write after restart, posts=%d patches=%d", posts, patches)
	}
	sub, err := db.GetNoditWebhookSubscriptionByCategory(ctx, "nodit", 8453, "transfer")
	if err != nil || sub == nil || !strings.HasPrefix(sub.SecretReference, webhookSealedKey) || strings.Contains(sub.SecretReference, secret) {
		t.Fatal("missing encrypted registration", err)
	}
	body := []byte(`{"subscriptionId":"123","sequenceNumber":"9","event":{"transactionHash":"` + testHashA + `"}}`)
	deliver := func(body []byte, delivery, key string) (WebhookAcceptance, error) {
		return service.AcceptWebhook(ctx, "transfer", map[string][]string{"X-Signature": {signWebhook(body, key)}, "X-Request-ID": {delivery}}, body)
	}
	if _, err := deliver(body, "123:9", "unverified"); !errors.Is(err, ErrInvalidSignature) {
		t.Fatal("unverified first attempt accepted", err)
	}
	first, err := deliver(body, "try-1", secret)
	if err != nil || !first.Created {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := deliver(body, "try-2", secret)
	if err != nil || !second.Duplicate || second.InboxID != first.InboxID {
		t.Fatalf("redelivery=%+v err=%v", second, err)
	}
	withoutSequence := []byte(`{"subscriptionId":"123","event":{"transactionHash":"` + testHashA + `"}}`)
	noSequence, err := deliver(withoutSequence, "attempt-3", secret)
	if err != nil || !noSequence.Created {
		t.Fatalf("unsequenced delivery=%+v err=%v", noSequence, err)
	}
	retry, err := deliver(withoutSequence, "attempt-4", secret)
	if err != nil || !retry.Duplicate || noSequence.InboxID != retry.InboxID {
		t.Fatalf("unsequenced redelivery=%+v err=%v", retry, err)
	}
	if _, err = deliver(body, "wrong-key", "wrong"); !errors.Is(err, ErrInvalidSignature) {
		t.Fatal("wrong signing key accepted", err)
	}
	if _, err = deliver(bytes.Replace(body, []byte("123"), []byte("145"), 1), "foreign-id", secret); !errors.Is(err, ErrInvalidWebhook) {
		t.Fatal("wrong subscription accepted", err)
	}
	createProject("0x2222222222222222222222222222222222222222")
	sync()
	if patches != 1 || !strings.Contains(string(registered.Condition), "22222222") {
		t.Fatal("second token not shared via PATCH")
	}
	setProjectStatus := func(status string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE market_projects SET status=$1 WHERE debox_user_id=$2`, status, owner); err != nil {
			t.Fatal(err)
		}
	}
	setProjectStatus("paused")
	sync()
	sync()
	if patches != 2 || registered.IsActive {
		t.Fatal("last target pause did not stop delivery once")
	}
	setProjectStatus("active")
	sync()
	if patches != 3 || posts != 2 || !registered.IsActive {
		t.Fatal("restore did not reuse the existing subscription")
	}
	// Externally missing: allow time for a just-created subscription to become
	// visible before recreating. Simulate a lost successful POST response.
	service.now = func() time.Time { return time.Now().Add(time.Hour) }
	registered = nil
	statusToReturn = 503
	if err := service.SyncWebhookSubscriptions(ctx); err == nil {
		t.Fatal("ambiguous outcome accepted")
	}
	cfg.WebhookSigningKeys = map[string]string{"base:transfer": "outdated-key"}
	service = New(deps, cfg)
	if _, err := deliver(body, "too-early", secret); !errors.Is(err, ErrWebhookUnavailable) {
		t.Fatal("creation intent accepted a delivery", err)
	}
	if err := service.SyncWebhookSubscriptions(ctx); !errors.Is(err, ErrWebhookUnavailable) || posts != 3 {
		t.Fatal("lost response reused an unbound key or issued second create", err)
	}
	cfg.WebhookSigningKeys["base:transfer"] = "123:" + secret
	service = New(deps, cfg)
	sync()
	if posts != 3 {
		t.Fatal("Console recovery recreated the subscription")
	}
	body = []byte(fmt.Sprintf(`{"subscriptionId":"123","sequenceNumber":10,"event":{"transactionHash":%q}}`, testHashA))
	if accepted, err := deliver(body, "recovered", secret); err != nil || !accepted.Created {
		t.Fatal("scoped Console key recovery failed", err)
	}
}
