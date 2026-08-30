package pipeline

import "strings"

// The grammars a remedy's interpolated pieces have to satisfy, and the check
// every command builder runs before it emits one.
//
// # Why a remedy is the string that gets a guard
//
// A remedy is the highest-stakes text this package produces, because it is
// built to be run. Every other field is read by a person who is deciding what
// to do; this one is pasted into a shell, and once bosun's read surfaces reach
// other agents it is pasted by something with no hands on the keyboard.
//
// The pieces interpolated into one come from a Kargo CRD this package
// deliberately does not vendor, through a controller release this build has
// never seen, and from a promotion target in somebody's values file. Nothing
// in this process authored them. So the rule is the narrow one: bosun composes
// every remedy from its own literals, and a piece it cannot vouch for costs
// the finding its remedy rather than producing a command with the piece in it.
//
// # Why it is expected never to fire
//
// Kubernetes validates these names itself: a namespace is an RFC1123 label, an
// object name an RFC1123 subdomain, and an object that reached the API server
// with anything else in its name does not exist. That is the point. The check
// is cheap precisely because it is always true, and the day it stops being
// true is the day an upstream assumption this package rests on has changed --
// which is worth finding as a missing remedy rather than as a command.
//
// # What is deliberately not here
//
// Quoting, escaping, or sanitising. A name that fails is not repaired into a
// safe one: there is no such repair, because the shell is not the only reader
// and a "safely quoted" `rm -rf /` is still the wrong Stage name. The finding
// is emitted without a remedy, which is a missing convenience; the alternative
// is a poisoned command, which is an incident.

// maxSegment is the longest an interpolated piece may be.
//
// 253 is the RFC1123 subdomain limit and therefore the longest legal
// Kubernetes object name; a path gets the same ceiling for want of a stricter
// one that a real repository would still satisfy.
const maxSegment = 253

// safeName reports whether a Kubernetes object name may be interpolated into a
// remedy.
//
// An RFC1123 subdomain: one or more labels joined by dots, each of them
// lowercase alphanumerics and dashes, each starting and ending alphanumeric.
// The dot is admitted rather than rejected because Kargo names a Promotion
// `<stage>.<ulid>.<short-sha>`, so a label-only check would take the remedy
// off every wedged Stage in production -- a control that causes the outage it
// was meant to prevent.
//
// The empty string is safe and means "not known": every builder replaces it
// with a placeholder bosun itself wrote before interpolating anything.
func safeName(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > maxSegment {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if !safeLabel(label) {
			return false
		}
	}
	return true
}

// safeLabel is the RFC1123 DNS label grammar itself: 1-63 characters of
// [a-z0-9-], first and last alphanumeric.
func safeLabel(s string) bool {
	if s == "" || len(s) > 63 || !within(s, "-", lowercase) {
		return false
	}
	return alnum(s[0], lowercase) && alnum(s[len(s)-1], lowercase)
}

// The two alphabets the grammars below draw from. A DNS label is lowercase by
// the RFC; a path and a key are not, because the things they name are not
// hostnames and a values file is full of camelCase.
const (
	lowercase = false
	anyCase   = true
)

// within reports whether every byte of s is alphanumeric or one of the
// punctuation characters in extra.
//
// One loop, three callers. It was three loops differing only in that string,
// which is how two of them would have drifted the first time somebody widened
// one -- and widening one of these is exactly the change that has to be made
// on purpose, in one place, with the reason written down.
func within(s, extra string, upper bool) bool {
	for i := 0; i < len(s); i++ {
		if !alnum(s[i], upper) && !strings.ContainsRune(extra, rune(s[i])) {
			return false
		}
	}
	return true
}

func alnum(c byte, upper bool) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	case upper && c >= 'A' && c <= 'Z':
		return true
	}
	return false
}

// safeNames is safeName over every argument, which is how a builder asks the
// question: all of them, or no remedy.
func safeNames(names ...string) bool {
	for _, n := range names {
		if !safeName(n) {
			return false
		}
	}
	return true
}

// safePath reports whether a repository path may be interpolated into a
// remedy.
//
// A different grammar from safeName because a file path is not an object name
// and Kubernetes validates none of it: it comes from a `yaml-update` step's
// `file:` in somebody's values file. So this is the narrowest grammar that
// still admits what a real one looks like -- `addons/kyverno/values.yaml` --
// and it admits nothing a shell, a glob or a path walk treats as an operator.
//
// No leading slash and no `..` segment: neither can appear in a path this
// package produced (Update.RepoPath has already stripped the clone prefix),
// and both are the shapes that turn a grep into a read of somewhere else.
func safePath(s string) bool {
	if s == "" || len(s) > maxSegment || strings.HasPrefix(s, "/") {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." || seg == ".." || !within(seg, ".-_", anyCase) {
			return false
		}
	}
	return true
}

// safeKey reports whether a promotion target's key may be interpolated into a
// remedy.
//
// The keys are yq paths -- `image.tag`, `controller.resources.limits.cpu` --
// and they reach the remedy as the expression `yq` is handed. Same reasoning
// as safePath: written by whoever wrote the values file, so the grammar admits
// what a key looks like and nothing that ends the quoted argument it sits in.
func safeKey(s string) bool {
	return s != "" && len(s) <= maxSegment && within(s, ".-_[]", anyCase)
}

// safeKeys is safeKey over every key a finding would interpolate.
func safeKeys(keys []string) bool {
	for _, k := range keys {
		if !safeKey(k) {
			return false
		}
	}
	return true
}
