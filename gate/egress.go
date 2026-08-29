package gate

import "github.com/JamesAtIntegratnIO/bosun/egress"

// EgressPolicy is the operator's deny-list, consulted before the gate lets a
// subprocess reach a remote chart repository.
//
// An interface rather than the egress package's type so the standalone
// gitops-gate binary keeps its short dependency list. egress.Policy satisfies
// it as-is, which is how the in-cluster service passes the same policy the
// triage side already applies to its own helm calls.
type EgressPolicy interface {
	// Denied reports whether a host is forbidden, and by which rule.
	Denied(host string) (rule string, denied bool)
}

// egressCheck is the gate's half of the accountability trade the egress package
// documents: reach anything, but say where you went, and let an operator forbid
// a destination by name.
//
// helm is a subprocess, so an HTTP transport cannot see inside it and the
// destination has to be checked and recorded here. It was not: the in-cluster
// gate is the default deployment and it pulled remote charts with no policy
// check and no log line, while config.go's EgressDeny comment and the start-up
// banner both described a control that covered every outbound request.
//
// Returns the reason to report when the host is denied, empty when the call may
// proceed. An unset policy is open, which is what the CLI wants.
func (c *Config) egressCheck(ref, chart, version string) string {
	host := egress.HostOf(ref)
	if host == "" {
		return ""
	}
	if c != nil && c.Egress != nil {
		if rule, denied := c.Egress.Denied(host); denied {
			return "egress to " + host + " is denied by policy (rule " + rule + ")"
		}
	}
	// Only the repository is known in advance; helm follows the index to
	// wherever the archive is served, and that hop is invisible from here. The
	// log says so rather than implying this is the whole story.
	if c != nil && c.Log != nil {
		c.Log("gate: outbound helm %s (chart %s %s; it will follow the index to wherever the archive is served)",
			host, chart, version)
	}
	return ""
}
