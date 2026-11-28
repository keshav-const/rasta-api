package main

import (
	"net/url"
	"testing"
)

// TestValidateQuery covers audit items 3.3/6.3: the read endpoints must reject
// malformed/abusive query params with a clear reason, while accepting every valid
// filter the consumer site and docs actually send.
func TestValidateQuery(t *testing.T) {
	ok := []url.Values{
		{},                                   // no params
		{"sort": {"trust"}},                  // allow-listed sort
		{"sort": {"freshness"}},              //
		{"city": {"port-blair"}},             // hyphenated slug
		{"country": {"India"}},               // capitalized, as stored
		{"category": {"street_food"}},        // underscored enum
		{"type": {"performing_art"}},         //
		{"q": {"blue pottery of jaipur"}},    // spaces are fine in search
		{"q": {"king's palace"}},             // apostrophe in free-text q is allowed (bound param)
		{"min_sources": {"3"}},               //
		{"limit": {"50"}, "offset": {"100"}}, //
		{"max_source_age_days": {"365"}},     //
	}
	for _, v := range ok {
		if msg := validateQuery(v); msg != "" {
			t.Errorf("validateQuery(%v) rejected a valid query: %s", v, msg)
		}
	}

	bad := []struct {
		name string
		v    url.Values
	}{
		{"unknown sort", url.Values{"sort": {"price"}}},
		{"sql-ish sort", url.Values{"sort": {"name; drop table poi"}}},
		{"oversized q", url.Values{"q": {string(make([]byte, maxQLen+1))}}},
		{"bad chars in city", url.Values{"city": {"jaipur'); --"}}},
		{"bad chars in type", url.Values{"type": {"a) or (1=1"}}},
		{"oversized filter", url.Values{"category": {string(make([]byte, maxFilterLen+1))}}},
		{"negative min_sources", url.Values{"min_sources": {"-1"}}},
		{"absurd min_sources", url.Values{"min_sources": {"999999"}}},
		{"nonnumeric min_platforms", url.Values{"min_platforms": {"lots"}}},
		{"zero limit", url.Values{"limit": {"0"}}},
		{"negative offset", url.Values{"offset": {"-5"}}},
		{"absurd offset", url.Values{"offset": {"100000000"}}},
		{"absurd age", url.Values{"max_source_age_days": {"99999999"}}},
	}
	for _, tc := range bad {
		if msg := validateQuery(tc.v); msg == "" {
			t.Errorf("validateQuery accepted an invalid query (%s): %v", tc.name, tc.v)
		}
	}
}
