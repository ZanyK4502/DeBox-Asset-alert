package marketparse

var (
	topicERC20Transfer = Topic("Transfer(address,address,uint256)")

	topicV2Swap        = Topic("Swap(address,uint256,uint256,uint256,uint256,address)")
	topicV2Mint        = Topic("Mint(address,uint256,uint256)")
	topicV2Burn        = Topic("Burn(address,uint256,uint256,address)")
	topicV2PairCreated = Topic("PairCreated(address,address,address,uint256)")

	topicV3Initialize  = Topic("Initialize(uint160,int24)")
	topicV3Swap        = Topic("Swap(address,address,int256,int256,uint160,uint128,int24)")
	topicPancakeV3Swap = Topic("Swap(address,address,int256,int256,uint160,uint128,int24,uint128,uint128)")
	topicV3Mint        = Topic("Mint(address,address,int24,int24,uint128,uint256,uint256)")
	topicV3Burn        = Topic("Burn(address,int24,int24,uint128,uint256,uint256)")
	topicV3PoolCreated = Topic("PoolCreated(address,address,uint24,int24,address)")

	topicInfinityCLInitialize  = Topic("Initialize(bytes32,address,address,address,uint24,bytes32,uint160,int24)")
	topicInfinityCLSwap        = Topic("Swap(bytes32,address,int128,int128,uint160,uint128,int24,uint24,uint16)")
	topicInfinityCLModify      = Topic("ModifyLiquidity(bytes32,address,int24,int24,int256,bytes32)")
	topicInfinityBinInitialize = Topic("Initialize(bytes32,address,address,address,uint24,bytes32,uint24)")
	topicInfinityBinSwap       = Topic("Swap(bytes32,address,int128,int128,uint24,uint24,uint16)")
	topicInfinityBinMint       = Topic("Mint(bytes32,address,uint256[],bytes32,bytes32[],bytes32,bytes32)")
	topicInfinityBinBurn       = Topic("Burn(bytes32,address,uint256[],bytes32,bytes32[])")

	topicFourTokenCreateV1 = Topic("TokenCreate(address,address,uint256,string,string,uint256,uint256,uint256)")
	topicFourPurchaseV1    = Topic("TokenPurchase(address,address,uint256,uint256,uint256)")
	topicFourSaleV1        = Topic("TokenSale(address,address,uint256,uint256,uint256)")
	topicFourTradeStop     = Topic("TradeStop(address)")

	// TokenCreate has the same canonical signature in TokenManager and TokenManager2.
	topicFourTokenCreateV2 = topicFourTokenCreateV1
	topicFourPurchaseV2    = Topic("TokenPurchase(address,address,uint256,uint256,uint256,uint256,uint256,uint256)")
	topicFourSaleV2        = Topic("TokenSale(address,address,uint256,uint256,uint256,uint256,uint256,uint256)")
	topicFourLiquidity     = Topic("LiquidityAdded(address,uint256,address,uint256)")
)

func V2FactoryEventTopic() string {
	return topicV2PairCreated
}

func V3FactoryEventTopic() string {
	return topicV3PoolCreated
}

func InfinityCLInitializeTopic() string {
	return topicInfinityCLInitialize
}

func InfinityBinInitializeTopic() string {
	return topicInfinityBinInitialize
}

func FourMemeEventTopics() []string {
	return []string{
		topicFourTokenCreateV1,
		topicFourPurchaseV1,
		topicFourSaleV1,
		topicFourPurchaseV2,
		topicFourSaleV2,
		topicFourTradeStop,
		topicFourLiquidity,
	}
}
