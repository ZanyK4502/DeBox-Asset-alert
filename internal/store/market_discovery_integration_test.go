package store

import (
	"context"
	"fmt"
	"testing"
)

func TestPostgresDiscoveryKeysetCoversSharedProjectsBeyondBoundedPage(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	for i := range 205 {
		// One project per synthetic owner, all sharing one chain/token.
		holderJobProject(t, database, 56, "bsc", tradeFixtureToken, fmt.Sprintf("discovery-owner-%d", i))
	}
	holderJobProject(t, database, 1, "ethereum", tradeFixtureToken, "another-chain")
	holderJobProject(t, database, 56, "bsc", tradeFixtureToken, "paused-owner")
	if _, err := pool.Exec(ctx, `UPDATE market_projects SET status='paused' WHERE debox_user_id='paused-owner'`); err != nil {
		t.Fatal(err)
	}
	var after MarketDiscoveryCursor
	seen := make(map[int64]bool)
	for batch := 0; batch < 3; batch++ {
		rows, err := database.ListMarketDiscoveryProjectsPage(ctx, 56, after, 999999)
		if err != nil || len(rows) > MaxMarketDiscoveryPageSize {
			t.Fatalf("bounded page rows=%d error=%v", len(rows), err)
		}
		more := len(rows) > 200
		if more {
			rows = rows[:200]
		}
		for _, project := range rows {
			if seen[project.ID] || project.ChainID != 56 || project.TokenAddress != tradeFixtureToken ||
				project.MarketProjectDeploymentID == nil || project.MarketAssetDeploymentID == nil {
				t.Fatalf("repeated, foreign or incomplete project identity: %+v", project)
			}
			seen[project.ID] = true
			after = MarketDiscoveryCursor{TokenAddress: project.TokenAddress, ProjectID: project.ID, ProjectDeploymentID: *project.MarketProjectDeploymentID}
		}
		if !more {
			break
		}
	}
	if len(seen) != 205 {
		t.Fatalf("keyset omitted active shared projects: %d", len(seen))
	}
}
