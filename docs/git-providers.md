# Git providers

Deliberately small: it carries what the workflow needs and nothing more. The
methods group into four jobs — read a pull request, read what has been said on
it, say something, and push a fix. `gitprovider/provider.go` is the list; a
count repeated here would go stale, and this one had (it said six against an
interface of ten).
(ADR 0004 committed to four; reading a pull request and listing its comments
turned out to be workflow needs too, which is the kind of growth the ADR's
"lowest common denominator" cost paragraph predicted.)

**On identity.** A token grants the right access and the wrong name: it belongs
to whoever minted it, so the agent's comments arrive under that person's avatar
and read like a colleague's until the footer. On GitHub, set `git.app.appId` and
give it a private key -- an App comments as `yourapp[bot]`, with a face of its
own, and its installation tokens expire hourly instead of never. On Gitea,
create a dedicated bot user and mint the token as that user; `local/` does
exactly this.

```go
GetPullRequest(ctx, number)          // title, branch, head SHA, labels
ListComments(ctx, number)            // to find the gate's report
CheckStatus(ctx, sha, checkName)     // pending | success | failure | missing
Comment(ctx, number, body)
AddLabel(ctx, number, label)         // the attempt cap lives here
PushFix(ctx, pr, root, message)      // to the PR's branch, never the default
```

| Provider | Status |
|---|---|
| GitHub | implemented, exercised |
| Gitea | implemented, exercised against a live instance |
| GitLab | extension point |
| Bitbucket | extension point |

`GIT_API_BASE` means different things per provider, because the providers do:
on GitHub it is the API root (`.../api/v3` for Enterprise); on Gitea it is the
**instance** root, because the client appends `/api/v1` itself and also needs
that root to build a push remote.

## Things a new implementation has to get right

**The gate's report is a comment.** A comment is the only artifact surface
every git host has, so the gate publishes there rather than into a
provider-specific artifact store. `ListComments` finds it by an HTML marker.

**Check state has two surfaces.** On GitHub a gate may report as a check run
(Actions) or a legacy commit status, and a repository can use either. Reading
only one makes a gate you did not look at indistinguishable from no gate.
Expect the same split elsewhere.

**Pushes must not re-trigger nothing.** Most hosts suppress workflow triggers
for pushes made with the CI system's own token. If the agent pushes with that
token, the gate never re-runs, the status stays red at its previous
conclusion, and the promotion waits on a result that will never change. Use a
separate credential.

**Never implement a merge or a close.** The interface deliberately has neither.
The agent proposes; the gate and the merge policy dispose.

## Token permissions

For GitHub, a fine-grained token scoped to the repository:

| Permission | Level | Why |
|---|---|---|
| Contents | read & write | push the fix to the bot branch |
| Pull requests | read & write | comment |
| Issues | read & write | labels, which carry the attempt cap |
| Commit statuses | read **and write** | read the gate, and publish the agent's own verdict beside it |
| Metadata | read | required baseline |
| Workflows | **none** | without it GitHub *rejects* any push touching `.github/workflows/**`, making "the agent cannot edit the gate" a server-side guarantee as well as a local one |

That last row is worth keeping even though `edits.DefaultDeny` already refuses
those paths. Two independent mechanisms, one of which is enforced by someone
else's server.
