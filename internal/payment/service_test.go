package payment

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const (
	testPayer     = "0x1111111111111111111111111111111111111111"
	testRecipient = "0x2222222222222222222222222222222222222222"
	testOther     = "0x3333333333333333333333333333333333333333"
	testHash      = "0xabababababababababababababababababababababababababababababababab"
)

type fakeRepository struct {
	createdParams store.CreateOrderParams
	createdOrder  store.Order
	createErr     error
	claimedInput  struct {
		orderID         int64
		deboxUserID     string
		payerAddress    string
		transactionHash string
	}
	claimedOrder store.Order
	claimErr     error
	updated      store.UpdateOrderVerificationParams
	finalized    store.FinalizedOrder
	finalizeArgs struct {
		orderID          int64
		transactionHash  string
		blockNumber      int64
		confirmations    int
		subscriptionDays int
	}
}

func (f *fakeRepository) CreateOrder(
	_ context.Context,
	params store.CreateOrderParams,
) (store.Order, error) {
	f.createdParams = params
	if f.createErr != nil {
		return store.Order{}, f.createErr
	}
	return f.createdOrder, nil
}

func (f *fakeRepository) ClaimOrderTransaction(
	_ context.Context,
	orderID int64,
	deboxUserID string,
	payerAddress string,
	transactionHash string,
) (store.Order, error) {
	f.claimedInput.orderID = orderID
	f.claimedInput.deboxUserID = deboxUserID
	f.claimedInput.payerAddress = payerAddress
	f.claimedInput.transactionHash = transactionHash
	if f.claimErr != nil {
		return store.Order{}, f.claimErr
	}
	return f.claimedOrder, nil
}

func (f *fakeRepository) UpdateOrderVerification(
	_ context.Context,
	_ int64,
	params store.UpdateOrderVerificationParams,
) (store.Order, error) {
	f.updated = params
	order := f.claimedOrder
	order.Status = params.Status
	order.TxConfirmations = int32(params.Confirmations)
	order.TxBlockNumber = params.BlockNumber
	order.VerificationError = params.Error
	return order, nil
}

func (f *fakeRepository) FinalizePaidOrder(
	_ context.Context,
	orderID int64,
	transactionHash string,
	blockNumber int64,
	confirmations int,
	subscriptionDays int,
) (store.FinalizedOrder, error) {
	f.finalizeArgs.orderID = orderID
	f.finalizeArgs.transactionHash = transactionHash
	f.finalizeArgs.blockNumber = blockNumber
	f.finalizeArgs.confirmations = confirmations
	f.finalizeArgs.subscriptionDays = subscriptionDays
	return f.finalized, nil
}

type fakeBlockchain struct {
	transaction    map[string]any
	receipt        map[string]any
	latestBlock    uint64
	transactionErr error
	receiptErr     error
	latestErr      error
}

func (f *fakeBlockchain) RPCTransactionByHash(
	context.Context,
	string,
	string,
	string,
) (map[string]any, error) {
	return f.transaction, f.transactionErr
}

func (f *fakeBlockchain) TransactionReceipt(
	context.Context,
	string,
	string,
	string,
) (map[string]any, error) {
	return f.receipt, f.receiptErr
}

func (f *fakeBlockchain) LatestBlockNumber(
	context.Context,
	string,
	string,
) (uint64, error) {
	return f.latestBlock, f.latestErr
}

func TestConfigurationReportsLivePaymentReadiness(t *testing.T) {
	service := testService(t, &fakeRepository{}, &fakeBlockchain{}, Settings{
		Mode:             "live",
		RecipientAddress: testRecipient,
		TokenAddress:     BSCUSDTAddress,
		TokenSymbol:      "USDT",
		TokenDecimals:    18,
	})
	configuration, err := service.Configuration(plans.Standard)
	if err != nil {
		t.Fatalf("Configuration() error = %v", err)
	}
	if !configuration.Ready ||
		configuration.ChainID != PaymentChainID ||
		configuration.TotalAmount != "10" ||
		configuration.RequiredConfirmations != RequiredConfirmations {
		t.Fatalf("unexpected configuration: %#v", configuration)
	}

	invalid := testService(t, &fakeRepository{}, &fakeBlockchain{}, Settings{
		Mode:          "live",
		TokenAddress:  testOther,
		TokenDecimals: 6,
	})
	configuration, err = invalid.Configuration(plans.Standard)
	if err != nil {
		t.Fatalf("invalid Configuration() error = %v", err)
	}
	if configuration.Ready || len(configuration.Missing) != 3 {
		t.Fatalf("invalid configuration = %#v", configuration)
	}
}

func TestPrepareBuildsExactUSDTTransferFromAuthenticatedIdentity(t *testing.T) {
	repository := &fakeRepository{createdOrder: store.Order{ID: 7}}
	service := testService(t, repository, &fakeBlockchain{}, liveSettings())

	result, err := service.Prepare(
		context.Background(),
		"user-1",
		testPayer,
		plans.Standard,
	)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	expectedUnits := new(big.Int).Exp(big.NewInt(10), big.NewInt(19), nil)
	expectedData, err := chain.EncodeERC20Transfer(testRecipient, expectedUnits)
	if err != nil {
		t.Fatalf("EncodeERC20Transfer() error = %v", err)
	}
	if repository.createdParams.DeBoxUserID != "user-1" ||
		repository.createdParams.PayerAddress != testPayer ||
		repository.createdParams.PlanCode != plans.Standard ||
		result.Transaction.From != testPayer ||
		result.Transaction.To != BSCUSDTAddress ||
		result.Transaction.Data != expectedData ||
		result.Transaction.Value != "0x0" ||
		result.AmountUnits != expectedUnits.String() {
		t.Fatalf("unexpected prepared payment: %#v / %#v", repository.createdParams, result)
	}
}

func TestPrepareRejectsPreviewFreeAndActivePlanConflict(t *testing.T) {
	preview := testService(t, &fakeRepository{}, &fakeBlockchain{}, Settings{
		Mode:        "preview",
		TokenSymbol: "USDT",
	})
	if _, err := preview.Prepare(context.Background(), "user-1", testPayer, plans.Standard); err == nil {
		t.Fatal("preview Prepare() error = nil")
	}

	live := testService(t, &fakeRepository{}, &fakeBlockchain{}, liveSettings())
	if _, err := live.Prepare(context.Background(), "user-1", testPayer, plans.Free); err == nil {
		t.Fatal("free Prepare() error = nil")
	}

	conflictRepository := &fakeRepository{createErr: store.ErrActiveSubscriptionConflict}
	live = testService(t, conflictRepository, &fakeBlockchain{}, liveSettings())
	if _, err := live.Prepare(context.Background(), "user-1", testPayer, plans.Standard); !errors.Is(
		err,
		store.ErrActiveSubscriptionConflict,
	) {
		t.Fatalf("conflict Prepare() error = %v", err)
	}
}

func TestVerifyKeepsPaymentConfirmingUntilThreeBlocks(t *testing.T) {
	repository := &fakeRepository{claimedOrder: testOrder()}
	blockchain := successfulBlockchain(t, 101, "10", testRecipient)
	service := testService(t, repository, blockchain, liveSettings())

	result, err := service.Verify(context.Background(), 7, testHash, "user-1", testPayer)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if result.PaymentStatus != "confirming" ||
		result.Confirmations != 2 ||
		repository.updated.Status != "confirming" ||
		repository.claimedInput.deboxUserID != "user-1" ||
		repository.claimedInput.payerAddress != testPayer {
		t.Fatalf("unexpected confirming result: %#v / %#v", result, repository)
	}
}

func TestVerifyFinalizesOnlyAfterThreeConfirmations(t *testing.T) {
	order := testOrder()
	paidOrder := order
	paidOrder.Status = "paid"
	subscription := &store.Subscription{PlanCode: plans.Standard, Status: "active"}
	repository := &fakeRepository{
		claimedOrder: order,
		finalized: store.FinalizedOrder{
			Order:        paidOrder,
			Subscription: subscription,
		},
	}
	service := testService(t, repository, successfulBlockchain(t, 102, "10", testRecipient), liveSettings())

	result, err := service.Verify(context.Background(), 7, testHash, "user-1", testPayer)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if result.PaymentStatus != "paid" ||
		result.Subscription != subscription ||
		repository.finalizeArgs.confirmations != RequiredConfirmations ||
		repository.finalizeArgs.subscriptionDays != 30 {
		t.Fatalf("unexpected paid result: %#v / %#v", result, repository.finalizeArgs)
	}
}

func TestVerifyRejectsMismatchedTransferDetails(t *testing.T) {
	tests := []struct {
		name       string
		order      store.Order
		blockchain *fakeBlockchain
	}{
		{
			name: "wrong order token",
			order: func() store.Order {
				order := testOrder()
				token := testOther
				order.TokenAddress = &token
				return order
			}(),
			blockchain: successfulBlockchain(t, 102, "10", testRecipient),
		},
		{
			name:       "wrong transaction token",
			order:      testOrder(),
			blockchain: successfulBlockchainWithToken(t, 102, "10", testRecipient, testOther),
		},
		{
			name:       "wrong recipient",
			order:      testOrder(),
			blockchain: successfulBlockchain(t, 102, "10", testOther),
		},
		{
			name:       "wrong amount",
			order:      testOrder(),
			blockchain: successfulBlockchain(t, 102, "9", testRecipient),
		},
		{
			name:  "failed receipt",
			order: testOrder(),
			blockchain: func() *fakeBlockchain {
				value := successfulBlockchain(t, 102, "10", testRecipient)
				value.receipt["status"] = "0x0"
				return value
			}(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeRepository{claimedOrder: test.order}
			service := testService(t, repository, test.blockchain, liveSettings())
			result, err := service.Verify(context.Background(), 7, testHash, "user-1", testPayer)
			if err != nil {
				t.Fatalf("Verify() error = %v", err)
			}
			if result.PaymentStatus != "failed" ||
				repository.updated.Status != "failed" ||
				result.Error == "" {
				t.Fatalf("unexpected failed result: %#v", result)
			}
		})
	}
}

func TestVerifyReportsNodeFailuresAsUnavailable(t *testing.T) {
	repository := &fakeRepository{claimedOrder: testOrder()}
	service := testService(t, repository, &fakeBlockchain{
		transactionErr: errors.New("timeout"),
	}, liveSettings())

	_, err := service.Verify(context.Background(), 7, testHash, "user-1", testPayer)
	if !errors.Is(err, ErrChainUnavailable) {
		t.Fatalf("Verify() error = %v", err)
	}
}

func testService(
	t *testing.T,
	repository Repository,
	blockchain Blockchain,
	settings Settings,
) *Service {
	t.Helper()
	catalog, err := plans.NewCatalog("10", 30, "USDT")
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	return New(repository, blockchain, catalog, settings)
}

func liveSettings() Settings {
	return Settings{
		Mode:             "live",
		RecipientAddress: testRecipient,
		TokenAddress:     BSCUSDTAddress,
		TokenSymbol:      "USDT",
		TokenDecimals:    18,
	}
}

func testOrder() store.Order {
	token := BSCUSDTAddress
	hash := testHash
	return store.Order{
		ID:               7,
		DeBoxUserID:      "user-1",
		PayerAddress:     testPayer,
		PlanCode:         plans.Standard,
		ChainKey:         PaymentChainKey,
		ChainID:          int32(PaymentChainID),
		TokenAddress:     &token,
		TokenSymbol:      "USDT",
		TokenDecimals:    18,
		TotalAmount:      "10",
		RecipientAddress: testRecipient,
		TxHash:           &hash,
		Status:           "confirming",
	}
}

func successfulBlockchain(
	t *testing.T,
	latestBlock uint64,
	amount string,
	recipient string,
) *fakeBlockchain {
	t.Helper()
	return successfulBlockchainWithToken(t, latestBlock, amount, recipient, BSCUSDTAddress)
}

func successfulBlockchainWithToken(
	t *testing.T,
	latestBlock uint64,
	amount string,
	recipient string,
	token string,
) *fakeBlockchain {
	t.Helper()
	units, err := chain.AmountToUnits(amount, 18)
	if err != nil {
		t.Fatalf("AmountToUnits() error = %v", err)
	}
	data, err := chain.EncodeERC20Transfer(recipient, units)
	if err != nil {
		t.Fatalf("EncodeERC20Transfer() error = %v", err)
	}
	return &fakeBlockchain{
		transaction: map[string]any{
			"hash":        testHash,
			"from":        testPayer,
			"to":          token,
			"value":       "0x0",
			"blockNumber": "0x64",
			"input":       data,
		},
		receipt: map[string]any{
			"transactionHash": testHash,
			"blockNumber":     "0x64",
			"status":          "0x1",
		},
		latestBlock: latestBlock,
	}
}
