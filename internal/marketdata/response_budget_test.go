package marketdata

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type responseBudgetDoer struct {
	read, closed, calls int
	text                string
}

type responseBudgetBody struct {
	owner *responseBudgetDoer
	r     io.Reader
}

func (b *responseBudgetBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.owner.read += n
	return n, err
}
func (b *responseBudgetBody) Close() error { b.owner.closed++; return nil }
func (d *responseBudgetDoer) Do(*http.Request) (*http.Response, error) {
	d.calls++
	return &http.Response{StatusCode: http.StatusOK, Body: &responseBudgetBody{owner: d, r: strings.NewReader(d.text)}, Header: http.Header{}}, nil
}

func TestDexScreenerWireResponseBudgetIsBoundedAndNeverTruncatesSuccess(t *testing.T) {
	for _, search := range []bool{false, true} {
		d := &responseBudgetDoer{text: strings.Repeat(" ", maxDexScreenerResponseBytes+1000)}
		client, err := NewDexScreenerClient("", WithDexScreenerHTTPClient(d), WithDexScreenerLimiter(noWaitLimiter{}))
		if err != nil {
			t.Fatal(err)
		}
		if search {
			_, err = client.SearchPairs(context.Background(), "test")
		} else {
			_, err = client.DiscoverPools(context.Background(), "bsc", marketTokenA)
		}
		if !errors.Is(err, ErrDexScreenerResponseTooLarge) || d.read != maxDexScreenerResponseBytes+1 || d.calls != 1 || d.closed != 1 {
			t.Fatalf("search=%v: err=%v read/calls/close=%d/%d/%d", search, err, d.read, d.calls, d.closed)
		}
	}
	// An exactly-at-limit valid document is accepted intact, including its last
	// byte; a limit+1 body must be rejected rather than decoding its prefix.
	valid := "[" + strings.Repeat(" ", maxDexScreenerResponseBytes-2) + "]"
	body, err := readDexScreenerBody(strings.NewReader(valid))
	if err != nil || string(body) != valid {
		t.Fatal("valid boundary response changed", err)
	}
}
