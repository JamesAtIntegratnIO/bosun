package gitprovider

import "strings"

// redactErr removes a credential from text that is about to be logged or
// posted into a pull-request comment.
//
// Shared by both providers rather than open-coded in each, which is how they
// came to differ: gitea called this, github inlined two ReplaceAll calls, and
// only one of them was reviewed the last time the rules changed.
//
// Only needed where the credential is in the text. A git push
// embeds it in the remote URL and git echoes that back on failure; an HTTP
// transport error carries a URL whose token lives in a header instead, so
// wrapping the error is both safe and more useful there.
func redactErr(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "***")
}
