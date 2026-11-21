package store

import (
	"context"
	"errors"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestMarketDiscoveryPageUsesBoundedKeysetAndPreservesQueryErrors(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	after := MarketDiscoveryCursor{TokenAddress: "0x1111111111111111111111111111111111111111", ProjectID: 7, ProjectDeploymentID: 11}
	mock.ExpectQuery(`(?s)FROM market_asset_deployments mad.*WHERE mad.chain_id=\$1.*\(mad.token_address,mp.id,mpd.id\)>\(\$2::text,\$3::bigint,\$4::bigint\).*ORDER BY mad.token_address,mp.id,mpd.id.*LIMIT \$5`).WithArgs(int64(56), after.TokenAddress, int64(7), int64(11), MaxMarketDiscoveryPageSize).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int64(8)))
	rows, err := newWithDB(mock).ListMarketDiscoveryProjectsPage(context.Background(), 56, after, 999999)
	if err != nil || len(rows) != 1 || rows[0].ID != 8 {
		t.Fatalf("bounded page=%+v %v", rows, err)
	}
	failure := errors.New("fixture read failure")
	mock.ExpectQuery(`(?s)SELECT.*LIMIT \$5`).WithArgs(int64(56), "", int64(0), int64(0), 1).WillReturnError(failure)
	if _, err := newWithDB(mock).ListMarketDiscoveryProjectsPage(context.Background(), 56, MarketDiscoveryCursor{}, 0); !errors.Is(err, failure) {
		t.Fatal("query failure swallowed", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newWithDB(nil).ListMarketDiscoveryProjectsPage(ctx, 56, MarketDiscoveryCursor{}, 200); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled query accessed database", err)
	}
}
