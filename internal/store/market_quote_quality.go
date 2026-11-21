package store

import "fmt"

// quoteAvailableSQL is the bounded-aggregate counterpart of
// marketquality.Quote.AvailableAt. Producers persist typed Quote JSON; missing
// provenance remains unavailable. It is evaluated at historical observation or
// valuation time, never at today's clock while building a historical digest.
// All arguments are fixed SQL expressions owned by this package, not user input.
func quoteAvailableSQL(quality, at, maximumAgeSeconds string) string {
	fetched := quoteTimestampSQL(quality + "->>'fetched_at'")
	sourceText := quality + "->>'source_timestamp'"
	source := quoteTimestampSQL("NULLIF(" + sourceText + ",'0001-01-01T00:00:00Z')")
	observed := "COALESCE(" + source + "," + fetched + ")"
	validSource := "(" + sourceText + " IS NULL OR " + sourceText + "='0001-01-01T00:00:00Z' OR " + source + " IS NOT NULL)"
	return fmt.Sprintf(`COALESCE((%s > 0 AND %s->>'status' IN ('live','cached')
		AND %s AND %s IS NOT NULL AND %s >= %s AND %s >= %s
		AND %s - %s <= %s * INTERVAL '1 second'
		AND %s - %s <= %s * INTERVAL '1 second'),FALSE)`,
		maximumAgeSeconds, quality, validSource, fetched, at, fetched, at, observed,
		at, fetched, maximumAgeSeconds, at, observed, maximumAgeSeconds)
}

// pg_input_is_valid is a PostgreSQL 16+ built-in (the deployed and isolated
// validation databases are PostgreSQL 18). CASE keeps corrupt historical JSON
// from aborting the digest. RFC3339 shape also rejects relative inputs such as
// "now" that PostgreSQL accepts but a typed marketquality.Quote never emits.
func quoteTimestampSQL(value string) string {
	return "(CASE WHEN (" + value + ") ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}([.][0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$' AND pg_catalog.pg_input_is_valid((" + value + "),'timestamp with time zone') THEN (" + value + ")::timestamptz END)"
}

func marketValuationAvailableSQL(alias, maximumAgeSeconds string) string {
	return "(" + alias + ".usd_value IS NOT NULL AND " + alias + ".metadata->>'valuation_schema_version'='1'" +
		" AND " + alias + ".metadata->>'valuation_complete'='true' AND " +
		quoteTimestampSQL(alias+".metadata->>'valuation_at'") + "=" + alias + ".occurred_at AND " +
		quoteAvailableSQL("("+alias+".metadata->'valuation_quote_quality')", alias+".occurred_at", maximumAgeSeconds) + ")"
}
