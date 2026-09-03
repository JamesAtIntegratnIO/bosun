package agent

import (
	"context"
	"strings"

	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/llm"
)

// What this process remembers about a handoff, and what it deliberately does
// not.
//
// The label is the handoff. It is the attempt cap's memory, it is what the
// next run reads to leave a pull request alone, and it is what handoff_queue
// selects on -- so it is written to the git host, where it survives this pod.
// The sentence the model gave for it is not: it goes on the commit status and
// into the log, and this file is the only place a caller can ask for it.
//
// Kept here in memory rather than written beside the label, because the two
// are worth different things. A handoff whose reason this process has
// forgotten is still a handoff -- the label says so, the comment on the pull
// request still carries what the agent said -- so there is nothing here worth
// a second write to the git host, and a read surface that finds no reason
// publishes none rather than inventing one.

// handOver puts a pull request in the queue that waits on a human, and keeps
// the model's own reason for it where a read surface can find it.
//
// One function for both, because they are one act. A path that labelled
// without remembering would escalate silently, and one that remembered without
// labelling would hold a reason for a pull request nobody was ever asked
// about.
//
// The label goes on first, and a refused one keeps nothing. A token with push
// permission and no permission to label is a real shape -- it is what made the
// attempt cap fail open once -- and on it the pull request never enters the
// queue at all, so a reason held for it is a sentence no reader can reach.
func (t *Triage) handOver(ctx context.Context, pr *gitprovider.PullRequest, v *llm.Verdict) error {
	if err := t.Git.AddLabel(ctx, pr.Number, labelNeedsHuman); err != nil {
		return err
	}
	t.rememberEscalation(pr.Number, v)
	return nil
}

// rememberEscalation keeps the model's own reason for a handoff, and only the
// model's.
//
// Guarded on the classification rather than on the string, and that guard is
// what the published field's origin rests on. A mechanical verdict may carry
// an escalationReason -- nothing in the schema forbids one -- and bosun
// escalates such a verdict on its own process facts, a push that failed or an
// attempt reserved too late. Publishing that leftover sentence beside a stop
// the model did not decide would tag a model's words as its account of a
// decision it never made.
//
// So: the model asked for the human, or nothing is held. Bosun's own reasons
// for stopping are published as bosun's, on the pull request, where they
// already are.
func (t *Triage) rememberEscalation(number int, v *llm.Verdict) {
	if v == nil || v.Classification != llm.ClassEscalate {
		return
	}
	reason := strings.TrimSpace(v.EscalationReason)
	if reason == "" {
		return
	}
	t.escalationsMu.Lock()
	defer t.escalationsMu.Unlock()
	if t.escalations == nil {
		t.escalations = map[int]string{}
	}
	t.escalations[number] = reason
}

// EscalationReason is what the model said when it asked for a human about one
// pull request, and "" when this process holds nothing for it.
//
// One return rather than a value and a flag, because rememberEscalation
// stores no empty string. A reason held is a reason with words in it, so ""
// and "not held" are the same answer, and a second return would offer a
// caller a distinction this type can never make true.
func (t *Triage) EscalationReason(number int) string {
	t.escalationsMu.Lock()
	defer t.escalationsMu.Unlock()
	return t.escalations[number]
}

// ForgetEscalationsExcept drops what is held for every pull request that is
// not in the list, which is how a reason leaves when its pull request does.
//
// Wired to gateservice.Service.Listed in the composition root, and that is the
// whole of its schedule. The sweep's listing is the only account in this
// process of a pull request having been merged or closed, and it happens
// whether or not anything is reading: a release driven by a read surface would
// keep one sentence per escalation for the life of a pod with that surface
// switched off, which is the default.
//
// Kept, they would be the slow leak the gate prunes its verdict cache and its
// comment histories to avoid, a few lines apart in the same sweep.
//
// It inherits that prune's race, and loses more to it. A pull request escalated
// after the sweep listed but before it publishes the listing is released
// immediately; the sweep's own dropped verdict is recomputed on the next run
// and this sentence is not, so it is gone until the agent triages that pull
// request again. The queue still names the pull request, because the label is
// on the git host, and the surface publishes an absent reason -- which is what
// absence there is defined to mean.
//
// A caller that has not listed must not call this. An empty list from a sweep
// that could not look would forget every reason this process holds, which is
// "nothing is open" and "nothing looked" confused in the direction that loses
// the work -- so Listed is called only where a listing happened, and that is
// where the rule is kept rather than here.
func (t *Triage) ForgetEscalationsExcept(open []int) {
	still := make(map[int]bool, len(open))
	for _, number := range open {
		still[number] = true
	}
	t.escalationsMu.Lock()
	defer t.escalationsMu.Unlock()
	for number := range t.escalations {
		if !still[number] {
			delete(t.escalations, number)
		}
	}
}
