package gitprovider

import "strings"

// redactErr removes a credential from text that is about to be logged or
// posted into a pull-request comment.
//
// Shared by both providers rather than open-coded in each, which is how they
// came to differ: gitea called this, github inlined two ReplaceAll calls, and
// only one of them was reviewed the last time the rules changed.
//
// Only needed where the credential could be in the text. No push embeds one
// in its remote URL any more, it travels in the environment now, but git
// quotes back whatever the server says and a misconfigured host can echo a
// credential it was sent, so what git prints on a failed push is still not
// safe to forward unread. An HTTP transport error carries only a URL, the
// token being in a header, so wrapping it is both safe and more useful there.
func redactErr(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "***")
}
