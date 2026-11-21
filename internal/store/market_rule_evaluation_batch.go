package store

// MarketRuleCurrentEventLimit bounds one atomic rule evaluation write. Current
// events are read in publication/event order, and only this complete prefix is
// committed with its watermark. Historical window reads keep their independent
// completeness and resource limits.
const MarketRuleCurrentEventLimit = 16
