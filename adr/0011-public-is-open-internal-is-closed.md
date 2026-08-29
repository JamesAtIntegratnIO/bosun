# 11. Public is open, internal is closed, and the dial decides which

- **Status:** accepted
- **Date:** 2026-08-29

## Context

`egress/` replaced an allow-list with a deny-list, and the reasoning is in its
package doc: naming every chart repository, every registry's blob CDN and every
redirect target before the agent could read it was a full-time job whose failure
mode was a two-minute timeout and a brief saying it had no evidence. Three
incidents added a host after the fact. The trade was deliberate — reach
anything, say where you went, let an operator forbid a destination by name —
and it is the right trade for a component whose whole job is reading public
metadata about public artifacts.

"Anything" was literal, and that is the part this record revises.

**Internal address space is the opposite case.** Nothing the agent reads lives
at `169.254.169.254`, on the cluster's own pod network, or on this pod's own
loopback. Reaching any of those is not a lookup that went somewhere unusual; it
is the agent pointed back at its own side of the network. And the destination is
frequently a caller's string: a promotion body's `artifact` field becomes an
outbound request, and the outcome comes back on a published pull-request
comment. Unbounded, that is a port scanner with a result page.

**A deny-list of host strings could never have closed it.** The same address is
`http://2852039166/`, it is `[::ffff:a9fe:a9fe]`, and it is any name whose owner
points it at the link-local block a moment after the string was checked.
Checking a name is checking one answer and connecting with the next.

**The NetworkPolicy was supposed to be the boundary, and it is one only on some
clusters.** `networkPolicy.egress.fqdns` and `fqdnPatterns` render exclusively
inside a `CiliumNetworkPolicy`; under the default `flavor: standard` they are
read by nothing, and the reachable answer there is
`egress.allowPublicHTTPS: true`, which is `0.0.0.0/0` on 443 with the internal
space excepted. `docs/safety-model.md` claimed the FQDN allow-list as an
enforced guarantee for every install, which was true for one flavor of one CNI.
A guarantee that depends on the operator's CNI is a guarantee this project
cannot make.

## Decision

**Two lists, and they are not the same kind of thing.**

`egress.Policy.Deny` (`triage.egressDeny`) stays what it was: public hosts an
operator has forbidden, matched on the host, with every outbound request and
every refusal logged. Accountability after the fact. The default is open and
stays open.

`egress.DefaultDenyNetworks` is new and is not a default with an override.
Loopback, link-local (169.254/16, and so the cloud metadata service), RFC1918,
CGNAT, and the IPv6 equivalents; an empty `Policy` has every one of them, and
the only way past one is to name it in `AllowPrivate`
(`triage.egressAllowPrivate`). This is `edits.DefaultDeny`'s shape, deliberately:
an operator opens a network by deciding to, never by forgetting to deny it.

**The rule lives on the dialler, not on the host string.** `net.Dialer.Control`
is handed the address that is about to be connected to, after resolution, which
is the only place the answer is a fact rather than a guess. That is what makes
it hold for a rebinding name, for a decimal-integer host, and for an
IPv4-mapped IPv6 spelling. The host string is still checked first, so a request
written straight at an address inside the closed space is refused before a
packet moves and the refusal can name the network an operator would have to
allow.

**Above both, the repository has to name the host.** Before an upstream lookup
runs, the checkout is searched for the host the promotion's `artifact` would
send the process to; nothing naming it means no lookup and a note saying so. A
substring search rather than a parse of a named field, because `repoURL` is Argo
CD's spelling and `spec.url` is Flux's, and picking one would be an assumption
about repository layout that Rule 1 does not allow.

## What it costs

**Three gaps stay open, and they are written down rather than hidden.** helm
runs as a subprocess: the gate checks a chart reference's host and logs it, and
then helm resolves and dials on its own, outside this dialler. Behind a proxy,
the address checked is the proxy's. A caller supplying a `RoundTripper` this
package cannot instrument keeps its own dialler and gets only the host check. A
Go process cannot portably impose a network namespace or a captive resolver on
its child, so the first of those is a NetworkPolicy question and will stay one.
The chart's answer is that `egress.allowPublicHTTPS` excepts the same internal
space this list closes, at the pod, where helm and git are inside it too — which
is why the two lists are written to match and should be changed together.

**An internal registry or proxy now needs naming.** An operator running a chart
museum on the cluster's own network, or whose egress proxy sits on an RFC1918
address, must set `triage.egressAllowPrivate`. This is the one deployment shape
that got worse. The symptom is a refusal that names the network to allow, which
is the failure mode worth having: the alternative shape of this mistake is a
hang with zero bytes.

**`AllowPrivate` takes addresses, not names.** The check runs where the address
is known, so an entry that parses as neither a CIDR nor an address allows
nothing. That is the safe direction and it shows up as the refusal continuing to
name the network, rather than as a rule that silently did not apply.

**One guarded transport per distinct set of allowed networks.** `Policy.Client`
is called per request and `upstream` builds one for every registry hop, so a
transport apiece would be a connection pool apiece, turning a walk of several
requests into several TLS handshakes that used to be one.

## Consequences

The safety model no longer states the FQDN allow-list as an unconditional
guarantee. It says what is enforced in Go on every install, what the chart adds
on Cilium, what it does not add on `standard`, and that the helm subprocess is
outside all of it. That is a weaker-sounding document describing a stronger
system, which is the correct direction for it to move.

Nothing about what the agent may *do* changed. It still writes only to the pull
request's own branch, still refuses the deny-listed paths, and still never
mutates the cluster. Narrowing what it may read is not narrowing what it may do,
any more than widening it was.
