package store

import (
	"context"
	"fmt"
	"time"
)

// MarketDiscoveryCursor is a keyset position, never an offset into a growing
// project list. Only this small position survives between discovery batches.
type MarketDiscoveryCursor struct {
	TokenAddress        string
	ProjectID           int64
	ProjectDeploymentID int64
}

const MaxMarketDiscoveryPageSize = 201 // 200 consumers plus one lookahead row

func (s *Store) ListMarketDiscoveryProjectsPage(ctx context.Context, chainID int64, after MarketDiscoveryCursor, limit int) ([]MarketProject, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Discovery needs identity and verified decimals, not potentially large
	// project metadata or supply evidence. Keep each page small at the source.
	projects, err := collectMany[MarketProject](ctx, s.db, `
		SELECT mp.id,mp.debox_user_id,mpd.id AS market_project_deployment_id,
		       mad.id AS market_asset_deployment_id,mad.chain_key,mad.chain_id,
		       mad.token_address,mad.token_decimals
		FROM market_asset_deployments mad
		JOIN market_project_deployments mpd
		  ON mpd.market_asset_deployment_id=mad.id AND mpd.status='active'
		JOIN market_projects mp ON mp.id=mpd.market_project_id AND mp.status='active'
		WHERE mad.chain_id=$1
		  AND (mad.token_address,mp.id,mpd.id)>($2::text,$3::bigint,$4::bigint)
		ORDER BY mad.token_address,mp.id,mpd.id
		LIMIT $5
	`, chainID, after.TokenAddress, after.ProjectID, after.ProjectDeploymentID, clamp(limit, 1, MaxMarketDiscoveryPageSize))
	if err != nil {
		return nil, fmt.Errorf("list market discovery project page: %w", err)
	}
	return projects, nil
}
