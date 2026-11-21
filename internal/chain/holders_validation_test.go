package chain

import (
	"context"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTokenHolderPagesRejectMissingMalformedAndOverBudgetFacts(t *testing.T) {
	a := "0x1111111111111111111111111111111111111111"
	b := "0x2222222222222222222222222222222222222222"
	overflow := new(big.Int).Lsh(big.NewInt(1), 256).String()
	for _, test := range []struct {
		name, body string
		rpp        int
	}{
		{"missing items", `{"count":1}`, 2},
		{"null items", `{"items":null}`, 2},
		{"wrong items type", `{"items":{}}`, 2},
		{"empty balance", fmt.Sprintf(`{"items":[{"ownerAddress":%q,"balance":""}]}`, a), 2},
		{"negative balance", fmt.Sprintf(`{"items":[{"ownerAddress":%q,"balance":"-1"}]}`, a), 2},
		{"signed positive balance", fmt.Sprintf(`{"items":[{"ownerAddress":%q,"balance":"+1"}]}`, a), 2},
		{"overflow balance", fmt.Sprintf(`{"items":[{"ownerAddress":%q,"balance":%q}]}`, a, overflow), 2},
		{"duplicate address", fmt.Sprintf(`{"items":[{"ownerAddress":%q,"balance":"1"},{"ownerAddress":%q,"balance":"2"}]}`, a, a), 2},
		{"too many items", fmt.Sprintf(`{"items":[{"ownerAddress":%q,"balance":"1"},{"ownerAddress":%q,"balance":"2"}]}`, a, b), 1},
		{"excess response rpp", `{"rpp":3,"items":[]}`, 2},
		{"negative count", `{"count":-1,"items":[]}`, 2},
		{"noninteger count", `{"count":1.5,"items":[]}`, 2},
		{"items exceed count", fmt.Sprintf(`{"count":0,"items":[{"ownerAddress":%q,"balance":"1"}]}`, a), 2},
		{"invalid cursor", `{"cursor":123,"items":[]}`, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { _, _ = io.WriteString(writer, test.body) }))
			defer server.Close()
			client, _ := NewClient("test", server.URL, WithHTTPClient(server.Client()))
			if page, err := client.TokenHoldersByContract(context.Background(), testToken, "bsc", "", TokenHoldersOptions{RPP: test.rpp, WithCount: true}); err == nil {
				t.Fatalf("bad holder facts accepted: %#v", page)
			}
		})
	}
}

func TestTokenHolderResponseByteLimitPreservesSharedUsageAndClient(t *testing.T) {
	for _, streamed := range []bool{false, true} {
		t.Run(fmt.Sprintf("streamed=%v", streamed), func(t *testing.T) {
			body := `{"items":[],"padding":"` + strings.Repeat("x", maxTokenHoldersResponseBytes) + `"}`
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if !streamed {
					writer.Header().Set("Content-Length", fmt.Sprint(len(body)))
				} else {
					writer.WriteHeader(http.StatusOK)
					writer.(http.Flusher).Flush()
				}
				_, _ = io.WriteString(writer, body)
			}))
			defer server.Close()
			calls := 0
			httpClient := server.Client()
			client, _ := NewClient("test", server.URL, WithHTTPClient(httpClient), WithUsageObserver(func(NoditUsage) { calls++ }))
			_, err := client.TokenHoldersByContract(context.Background(), testToken, "bsc", "", TokenHoldersOptions{RPP: 100})
			if err == nil || !strings.Contains(err.Error(), "byte budget") {
				t.Fatalf("oversized holder response accepted: %v", err)
			}
			if calls != 1 || client.httpClient != httpClient {
				t.Fatalf("holder wrapper replaced shared client/accounting: calls=%d", calls)
			}
		})
	}
}

func TestTokenHolderPageAcceptsExplicitEmptyAndExactUint256(t *testing.T) {
	maximum := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)).String()
	responses := []string{fmt.Sprintf(`{"rpp":2,"count":1,"items":[{"ownerAddress":"0x1111111111111111111111111111111111111111","balance":%q}]}`, maximum), `{"rpp":2,"count":0,"items":[]}`}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, responses[requests.Add(1)-1])
	}))
	defer server.Close()
	client, _ := NewClient("test", server.URL, WithHTTPClient(server.Client()))
	page, err := client.TokenHoldersByContract(context.Background(), testToken, "bsc", "", TokenHoldersOptions{RPP: 2, WithCount: true})
	if err != nil || len(page.Items) != 1 || page.Items[0].BalanceRaw != maximum {
		t.Fatalf("valid uint256 lost: %#v,%v", page, err)
	}
	page, err = client.TokenHoldersByContract(context.Background(), testToken, "bsc", "", TokenHoldersOptions{RPP: 2, WithCount: true})
	if err != nil || len(page.Items) != 0 || page.Count == nil || *page.Count != 0 {
		t.Fatalf("explicit empty page rejected: %#v,%v", page, err)
	}
}
