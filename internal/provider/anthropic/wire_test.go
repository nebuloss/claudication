package anthropic

import "testing"

func TestHasBeta(t *testing.T) {
	cases := map[string]bool{
		"":                        false,
		"oauth-2025-04-20":        true,
		"a, oauth-2025-04-20 ,b":  true,
		"OAUTH-2025-04-20":        true,
		"oauth-2025-04-20-extra":  false,
		"prefix-oauth-2025-04-20": false,
	}
	for header, want := range cases {
		if got := hasBeta(header, oauthBeta); got != want {
			t.Errorf("hasBeta(%q) = %v, want %v", header, got, want)
		}
	}
}
