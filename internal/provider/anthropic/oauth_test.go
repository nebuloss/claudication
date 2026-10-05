package anthropic

import (
	"strings"
	"testing"

	"claudication/internal/oauth"
)

func TestAnthropicAuthURL(t *testing.T) {
	pkce := oauth.PKCE{Verifier: "v", Challenge: "chal"}
	got := AuthURL("st4te", pkce, RedirectManual, "")

	// Pinned against a URL captured from `claude auth login --claudeai` on
	// 2.1.263 and confirmed working in a browser. Every deviation from this
	// shape that was tried — claude.ai as the host, scope moved last, colons
	// left unencoded — was rejected with "client_id: Field required", so this
	// is an exact-match assertion on purpose rather than a set of loose
	// Contains checks.
	want := "https://claude.com/cai/oauth/authorize" +
		"?code=true" +
		"&client_id=" + ClientID +
		"&response_type=code" +
		"&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback" +
		"&scope=org%3Acreate_api_key+user%3Aprofile+user%3Ainference" +
		"+user%3Asessions%3Aclaude_code+user%3Amcp_servers+user%3Afile_upload" +
		"&code_challenge=chal" +
		"&code_challenge_method=S256" +
		"&state=st4te"

	if got != want {
		t.Errorf("auth URL does not match the captured reference\n got:  %s\n want: %s", got, want)
	}
}

// The consent screen rejects a malformed request outright, so the parameter
// order is pinned to the client's rather than left to map iteration or
// alphabetical sorting.
func TestAnthropicAuthURLParameterOrder(t *testing.T) {
	got := AuthURL("st4te", oauth.PKCE{Challenge: "chal"}, RedirectManual, "")
	query := got[strings.Index(got, "?")+1:]

	var names []string
	for _, pair := range strings.Split(query, "&") {
		names = append(names, strings.SplitN(pair, "=", 2)[0])
	}
	want := []string{
		"code", "client_id", "response_type", "redirect_uri",
		"scope", "code_challenge", "code_challenge_method", "state",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("parameter order = %v, want %v", names, want)
	}
}

// A reconnect names the account, as `claude auth login --email` does: last,
// after state, and only when there is one to name.
func TestAuthURLCarriesTheLoginHint(t *testing.T) {
	got := AuthURL("st4te", oauth.PKCE{Challenge: "chal"}, RedirectManual, "me+work@example.com")
	if !strings.HasSuffix(got, "&state=st4te&login_hint=me%2Bwork%40example.com") {
		t.Errorf("login_hint missing, misplaced or unescaped: %s", got)
	}
	if strings.Contains(AuthURL("st4te", oauth.PKCE{}, RedirectManual, ""), "login_hint") {
		t.Error("an empty hint was sent")
	}
}
