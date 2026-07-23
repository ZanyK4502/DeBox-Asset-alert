package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const (
	PaymentChainKey       = "bsc"
	PaymentChainID        = int64(56)
	BSCUSDTAddress        = "0x55d398326f99059ff775485246999027b3197955"
	RequiredConfirmations = 3
)

var ErrChainUnavailable = errors.New("chain node is temporarily unavailable")

type Repository interface {
	ExpirePendingOrders(context.Context) (int64, error)
	ListConfirmingOrders(context.Context, int) ([]store.Order, error)
	CreateOrder(context.Context, store.CreateOrderParams) (store.Order, error)
	ClaimOrderTransaction(context.Context, int64, string, string, string) (store.Order, error)
	UpdateOrderVerification(
		context.Context,
		int64,
		store.UpdateOrderVerificationParams,
	) (store.Order, error)
	FinalizePaidOrder(context.Context, int64, string, int64, int, int) (store.FinalizedOrder, error)
}

type Blockchain interface {
	RPCTransactionByHash(context.Context, string, string, string) (map[string]any, error)
	TransactionReceipt(context.Context, string, string, string) (map[string]any, error)
	LatestBlockNumber(context.Context, string, string) (uint64, error)
}

type Settings struct {
	Mode             string
	RecipientAddress string
	TokenAddress     string
	TokenSymbol      string
	TokenDecimals    int
}

type Service struct {
	repository Repository
	blockchain Blockchain
	catalog    *plans.Catalog
	settings   Settings
}

func New(
	repository Repository,
	blockchain Blockchain,
	catalog *plans.Catalog,
	settings Settings,
) *Service {
	settings.Mode = strings.ToLower(strings.TrimSpace(settings.Mode))
	if settings.Mode == "" {
		settings.Mode = "preview"
	}
	settings.TokenSymbol = strings.TrimSpace(settings.TokenSymbol)
	return &Service{
		repository: repository,
		blockchain: blockchain,
		catalog:    catalog,
		settings:   settings,
	}
}

type Configuration struct {
	Mode                  string        `json:"mode"`
	Plan                  plans.Plan    `json:"plan"`
	Chain                 chain.Profile `json:"chain"`
	ChainName             string        `json:"chain_name"`
	ChainID               int64         `json:"chain_id"`
	ChainIDHex            string        `json:"chain_id_hex"`
	Asset                 string        `json:"asset"`
	TokenAddress          string        `json:"token_address"`
	TokenDecimals         int           `json:"token_decimals"`
	TotalAmount           string        `json:"total_amount"`
	RecipientAddress      string        `json:"recipient_address"`
	RequiredConfirmations int           `json:"required_confirmations"`
	Ready                 bool          `json:"ready"`
	Missing               []string      `json:"missing"`
}

func (s *Service) Configuration(planCode string) (Configuration, error) {
	plan, err := s.catalog.Get(planCode)
	if err != nil {
		return Configuration{}, err
	}
	profile, err := chain.ChainProfile(PaymentChainKey, PaymentChainKey)
	if err != nil {
		return Configuration{}, err
	}

	missing := make([]string, 0)
	recipient, recipientErr := chain.ValidateAddress(s.settings.RecipientAddress)
	if recipientErr != nil {
		recipient = s.settings.RecipientAddress
		if s.settings.Mode == "live" {
			missing = append(missing, "PAYMENT_RECIPIENT_ADDRESS")
		}
	}
	token, tokenErr := chain.ValidateAddress(s.settings.TokenAddress)
	if tokenErr != nil {
		token = s.settings.TokenAddress
		if s.settings.Mode == "live" {
			missing = append(missing, "SUBSCRIPTION_TOKEN_ADDRESS")
		}
	}
	if s.settings.Mode == "live" && tokenErr == nil && token != BSCUSDTAddress {
		missing = append(missing, "SUBSCRIPTION_TOKEN_ADDRESS must be BSC USDT")
	}
	if s.settings.Mode == "live" && s.settings.TokenDecimals != 18 {
		missing = append(missing, "SUBSCRIPTION_TOKEN_DECIMALS must be 18")
	}

	return Configuration{
		Mode:                  s.settings.Mode,
		Plan:                  plan,
		Chain:                 profile,
		ChainName:             profile.Name,
		ChainID:               profile.ChainID,
		ChainIDHex:            profile.ChainIDHex,
		Asset:                 s.settings.TokenSymbol,
		TokenAddress:          token,
		TokenDecimals:         s.settings.TokenDecimals,
		TotalAmount:           plan.Price,
		RecipientAddress:      recipient,
		RequiredConfirmations: RequiredConfirmations,
		Ready:                 len(missing) == 0,
		Missing:               missing,
	}, nil
}

type TransactionRequest struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Data    string `json:"data"`
	Value   string `json:"value"`
	ChainID string `json:"chainId"`
}

type TransactionEnvelope struct {
	Request TransactionRequest `json:"request"`
}

type PrepareResult struct {
	Order                 store.Order           `json:"order"`
	Chain                 chain.Profile         `json:"chain"`
	Transaction           TransactionRequest    `json:"transaction"`
	Transactions          []TransactionEnvelope `json:"transactions"`
	AmountUnits           string                `json:"amount_units"`
	Amount                string                `json:"amount"`
	Symbol                string                `json:"symbol"`
	Recipient             string                `json:"recipient"`
	RequiredConfirmations int                   `json:"required_confirmations"`
}

func (s *Service) Prepare(
	ctx context.Context,
	deboxUserID string,
	payerAddress string,
	planCode string,
) (PrepareResult, error) {
	userID := strings.TrimSpace(deboxUserID)
	if userID == "" {
		return PrepareResult{}, errors.New("missing DeBox user identity")
	}
	configuration, err := s.Configuration(planCode)
	if err != nil {
		return PrepareResult{}, err
	}
	if configuration.Plan.Code == plans.Free {
		return PrepareResult{}, errors.New("the free plan does not require payment")
	}
	if configuration.Mode != "live" {
		return PrepareResult{}, errors.New("payment is currently in preview mode")
	}
	if !configuration.Ready {
		return PrepareResult{}, fmt.Errorf(
			"payment configuration is incomplete: %s",
			strings.Join(configuration.Missing, ", "),
		)
	}
	payer, err := chain.ValidateAddress(payerAddress)
	if err != nil {
		return PrepareResult{}, err
	}
	recipient, err := chain.ValidateAddress(configuration.RecipientAddress)
	if err != nil {
		return PrepareResult{}, err
	}
	token, err := chain.ValidateAddress(configuration.TokenAddress)
	if err != nil {
		return PrepareResult{}, err
	}
	amountUnits, err := chain.AmountToUnits(
		configuration.Plan.Price,
		configuration.TokenDecimals,
	)
	if err != nil {
		return PrepareResult{}, err
	}
	data, err := chain.EncodeERC20Transfer(recipient, amountUnits)
	if err != nil {
		return PrepareResult{}, err
	}
	tokenAddress := token
	order, err := s.repository.CreateOrder(ctx, store.CreateOrderParams{
		DeBoxUserID:      userID,
		PayerAddress:     payer,
		PlanCode:         configuration.Plan.Code,
		ChainKey:         configuration.Chain.Key,
		ChainID:          int32(configuration.Chain.ChainID),
		TokenAddress:     &tokenAddress,
		TokenSymbol:      configuration.Asset,
		TokenDecimals:    int32(configuration.TokenDecimals),
		TotalAmount:      configuration.Plan.Price,
		RecipientAddress: recipient,
	})
	if err != nil {
		return PrepareResult{}, err
	}
	request := TransactionRequest{
		From:    payer,
		To:      token,
		Data:    data,
		Value:   "0x0",
		ChainID: configuration.Chain.ChainIDHex,
	}
	return PrepareResult{
		Order:                 order,
		Chain:                 configuration.Chain,
		Transaction:           request,
		Transactions:          []TransactionEnvelope{{Request: request}},
		AmountUnits:           amountUnits.String(),
		Amount:                configuration.Plan.Price,
		Symbol:                configuration.Asset,
		Recipient:             recipient,
		RequiredConfirmations: RequiredConfirmations,
	}, nil
}

type VerifyResult struct {
	PaymentStatus         string              `json:"payment_status"`
	Order                 store.Order         `json:"order"`
	Subscription          *store.Subscription `json:"subscription,omitempty"`
	Confirmations         int                 `json:"confirmations"`
	RequiredConfirmations int                 `json:"required_confirmations"`
	Error                 string              `json:"error,omitempty"`
}

type ReconciliationError struct {
	OrderID int64  `json:"order_id"`
	Error   string `json:"error"`
}

type ReconciliationResult struct {
	Checked    int                   `json:"checked"`
	Expired    int64                 `json:"expired"`
	Paid       int                   `json:"paid"`
	Confirming int                   `json:"confirming"`
	Failed     int                   `json:"failed"`
	Errors     []ReconciliationError `json:"errors"`
}

func (s *Service) Reconcile(ctx context.Context, limit int) (ReconciliationResult, error) {
	expired, err := s.repository.ExpirePendingOrders(ctx)
	if err != nil {
		return ReconciliationResult{}, err
	}
	orders, err := s.repository.ListConfirmingOrders(ctx, limit)
	if err != nil {
		return ReconciliationResult{}, err
	}
	result := ReconciliationResult{
		Expired: expired,
		Errors:  make([]ReconciliationError, 0),
	}
	for _, order := range orders {
		verification, verifyErr := s.verifyClaimedOrder(ctx, order)
		result.Checked++
		if verifyErr != nil {
			if _, updateErr := s.repository.UpdateOrderVerification(
				ctx,
				order.ID,
				store.UpdateOrderVerificationParams{
					Status:        "confirming",
					BlockNumber:   order.TxBlockNumber,
					Confirmations: int(order.TxConfirmations),
					Error:         verifyErr.Error(),
				},
			); updateErr != nil {
				verifyErr = errors.Join(verifyErr, updateErr)
			}
			result.Errors = append(result.Errors, ReconciliationError{
				OrderID: order.ID,
				Error:   verifyErr.Error(),
			})
			continue
		}
		switch verification.PaymentStatus {
		case "paid":
			result.Paid++
		case "failed":
			result.Failed++
		default:
			result.Confirming++
		}
	}
	return result, nil
}

func (s *Service) Verify(
	ctx context.Context,
	orderID int64,
	txHash string,
	deboxUserID string,
	payerAddress string,
) (VerifyResult, error) {
	hash, err := chain.ValidateTransactionHash(txHash)
	if err != nil {
		return VerifyResult{}, err
	}
	payer, err := chain.ValidateAddress(payerAddress)
	if err != nil {
		return VerifyResult{}, err
	}
	order, err := s.repository.ClaimOrderTransaction(
		ctx,
		orderID,
		strings.TrimSpace(deboxUserID),
		payer,
		hash,
	)
	if err != nil {
		return VerifyResult{}, err
	}
	if order.Status == "paid" {
		confirmations := int(order.TxConfirmations)
		if confirmations == 0 {
			confirmations = RequiredConfirmations
		}
		return VerifyResult{
			PaymentStatus:         "paid",
			Order:                 order,
			Confirmations:         confirmations,
			RequiredConfirmations: RequiredConfirmations,
		}, nil
	}
	return s.verifyClaimedOrder(ctx, order)
}

func (s *Service) verifyClaimedOrder(
	ctx context.Context,
	order store.Order,
) (VerifyResult, error) {
	hash, err := orderHash(order)
	if err != nil {
		return VerifyResult{}, err
	}
	if order.ChainKey != PaymentChainKey || int64(order.ChainID) != PaymentChainID {
		return s.failed(ctx, order, "order payment network is not BNB Chain", nil)
	}
	token, err := optionalAddress(order.TokenAddress)
	if err != nil || token != BSCUSDTAddress {
		return s.failed(ctx, order, "order payment token is not BSC USDT", nil)
	}

	transaction, err := s.blockchain.RPCTransactionByHash(
		ctx,
		hash,
		PaymentChainKey,
		PaymentChainKey,
	)
	if err != nil {
		return VerifyResult{}, chainUnavailable(err)
	}
	receipt, err := s.blockchain.TransactionReceipt(
		ctx,
		hash,
		PaymentChainKey,
		PaymentChainKey,
	)
	if err != nil {
		return VerifyResult{}, chainUnavailable(err)
	}
	if transaction == nil || receipt == nil {
		return s.confirming(ctx, order, nil, 0)
	}

	blockNumber, err := hexQuantity(receipt["blockNumber"], "blockNumber")
	if err != nil {
		return VerifyResult{}, err
	}
	if blockNumber > math.MaxInt64 {
		return s.failed(ctx, order, "transaction block number is invalid", nil)
	}
	block := int64(blockNumber)
	if err := validateTransaction(order, hash, transaction, receipt, blockNumber); err != nil {
		return s.failed(ctx, order, err.Error(), &block)
	}

	recipient, amountUnits, err := decodeERC20Transfer(transaction["input"])
	if err != nil {
		return s.failed(ctx, order, err.Error(), &block)
	}
	expectedRecipient, err := chain.ValidateAddress(order.RecipientAddress)
	if err != nil || recipient != expectedRecipient {
		return s.failed(ctx, order, "USDT recipient does not match the order", &block)
	}
	expectedAmount, err := chain.AmountToUnits(order.TotalAmount, int(order.TokenDecimals))
	if err != nil {
		return VerifyResult{}, err
	}
	if amountUnits.Cmp(expectedAmount) != 0 {
		return s.failed(ctx, order, "USDT amount does not match the order", &block)
	}

	latestBlock, err := s.blockchain.LatestBlockNumber(
		ctx,
		PaymentChainKey,
		PaymentChainKey,
	)
	if err != nil {
		return VerifyResult{}, chainUnavailable(err)
	}
	confirmations := 0
	if latestBlock >= blockNumber {
		count := latestBlock - blockNumber + 1
		if count > math.MaxInt {
			confirmations = math.MaxInt
		} else {
			confirmations = int(count)
		}
	}
	if confirmations < RequiredConfirmations {
		return s.confirming(ctx, order, &block, confirmations)
	}

	plan, err := s.catalog.Get(order.PlanCode)
	if err != nil {
		return VerifyResult{}, err
	}
	finalized, err := s.repository.FinalizePaidOrder(
		ctx,
		order.ID,
		hash,
		block,
		confirmations,
		plan.Days,
	)
	if err != nil {
		return VerifyResult{}, err
	}
	return VerifyResult{
		PaymentStatus:         "paid",
		Order:                 finalized.Order,
		Subscription:          finalized.Subscription,
		Confirmations:         confirmations,
		RequiredConfirmations: RequiredConfirmations,
	}, nil
}

func validateTransaction(
	order store.Order,
	hash string,
	transaction map[string]any,
	receipt map[string]any,
	blockNumber uint64,
) error {
	transactionHash, err := chain.ValidateTransactionHash(valueString(transaction["hash"]))
	if err != nil || transactionHash != hash {
		return errors.New("on-chain transaction hash does not match")
	}
	receiptHash, err := chain.ValidateTransactionHash(valueString(receipt["transactionHash"]))
	if err != nil || receiptHash != hash {
		return errors.New("transaction receipt hash does not match")
	}
	transactionBlock, err := hexQuantity(transaction["blockNumber"], "blockNumber")
	if err != nil || transactionBlock != blockNumber {
		return errors.New("transaction and receipt block numbers do not match")
	}
	payer, err := chain.ValidateAddress(valueString(transaction["from"]))
	expectedPayer, expectedPayerErr := chain.ValidateAddress(order.PayerAddress)
	if err != nil || expectedPayerErr != nil || payer != expectedPayer {
		return errors.New("payment wallet does not match the order")
	}
	token, err := chain.ValidateAddress(valueString(transaction["to"]))
	expectedToken, expectedTokenErr := optionalAddress(order.TokenAddress)
	if err != nil || expectedTokenErr != nil || token != expectedToken {
		return errors.New("transaction was not sent to the required USDT contract")
	}
	value, err := hexQuantity(defaultValue(transaction["value"], "0x0"), "value")
	if err != nil || value != 0 {
		return errors.New("USDT payment must not include a BNB value")
	}
	status, err := hexQuantity(receipt["status"], "status")
	if err != nil || status != 1 {
		return errors.New("on-chain payment transaction failed")
	}
	return nil
}

func (s *Service) confirming(
	ctx context.Context,
	order store.Order,
	blockNumber *int64,
	confirmations int,
) (VerifyResult, error) {
	updated, err := s.repository.UpdateOrderVerification(
		ctx,
		order.ID,
		store.UpdateOrderVerificationParams{
			Status:        "confirming",
			BlockNumber:   blockNumber,
			Confirmations: confirmations,
		},
	)
	if err != nil {
		return VerifyResult{}, err
	}
	return VerifyResult{
		PaymentStatus:         "confirming",
		Order:                 updated,
		Confirmations:         confirmations,
		RequiredConfirmations: RequiredConfirmations,
	}, nil
}

func (s *Service) failed(
	ctx context.Context,
	order store.Order,
	message string,
	blockNumber *int64,
) (VerifyResult, error) {
	updated, err := s.repository.UpdateOrderVerification(
		ctx,
		order.ID,
		store.UpdateOrderVerificationParams{
			Status:      "failed",
			BlockNumber: blockNumber,
			Error:       message,
		},
	)
	if err != nil {
		return VerifyResult{}, err
	}
	return VerifyResult{
		PaymentStatus:         "failed",
		Order:                 updated,
		Confirmations:         0,
		RequiredConfirmations: RequiredConfirmations,
		Error:                 message,
	}, nil
}

func orderHash(order store.Order) (string, error) {
	if order.TxHash == nil {
		return "", errors.New("order transaction hash is missing")
	}
	return chain.ValidateTransactionHash(*order.TxHash)
}

func optionalAddress(value *string) (string, error) {
	if value == nil {
		return "", errors.New("address is missing")
	}
	return chain.ValidateAddress(*value)
}

func hexQuantity(value any, fieldName string) (uint64, error) {
	switch typed := value.(type) {
	case string:
		if !strings.HasPrefix(typed, "0x") || len(typed) <= 2 {
			break
		}
		number, err := strconv.ParseUint(typed[2:], 16, 64)
		if err == nil {
			return number, nil
		}
	case int:
		if typed >= 0 {
			return uint64(typed), nil
		}
	case int64:
		if typed >= 0 {
			return uint64(typed), nil
		}
	case uint64:
		return typed, nil
	case json.Number:
		number, err := strconv.ParseUint(typed.String(), 10, 64)
		if err == nil {
			return number, nil
		}
	}
	return 0, fmt.Errorf("invalid transaction field %s", fieldName)
}

func decodeERC20Transfer(data any) (string, *big.Int, error) {
	value := strings.ToLower(strings.TrimSpace(valueString(data)))
	if len(value) != 138 || !strings.HasPrefix(value, "0xa9059cbb") {
		return "", nil, errors.New("transaction is not a standard ERC-20 transfer")
	}
	recipientWord := value[10:74]
	if recipientWord[:24] != strings.Repeat("0", 24) {
		return "", nil, errors.New("transaction recipient encoding is invalid")
	}
	recipient, err := chain.ValidateAddress("0x" + recipientWord[24:])
	if err != nil {
		return "", nil, errors.New("transaction transfer parameters are invalid")
	}
	amount := new(big.Int)
	if _, ok := amount.SetString(value[74:138], 16); !ok {
		return "", nil, errors.New("transaction transfer parameters are invalid")
	}
	return recipient, amount, nil
}

func defaultValue(value any, fallback any) any {
	if value == nil {
		return fallback
	}
	return value
}

func valueString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func chainUnavailable(err error) error {
	return fmt.Errorf("%w: %v", ErrChainUnavailable, err)
}
