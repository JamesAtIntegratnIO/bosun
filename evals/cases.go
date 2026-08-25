// Package evals holds the triage cases the prompt is measured against.
//
// Every case is a real incident from a production GitOps repository, not an
// invented one. That matters: the failures worth catching are the ones where
// a pull request renders perfectly and breaks at runtime, and those have a
// particular shape that made-up examples do not reproduce.
package evals

import (
	"sort"

	"github.com/JamesAtIntegratnIO/bosun/upstream"
)

// Path names which prompt a case measures. Two prompts ship and they fail
// differently: triage can write the wrong thing to a file, and explain can
// write the wrong thing into a reader's head. Only the first has an applier
// standing in front of it, which is precisely why the second needs measuring.
const (
	// PathTriage is the default: the red-gate classifier, scored on what the
	// applier would actually have written.
	PathTriage = ""
	// PathExplain is the green-gate explanation, scored on grounding.
	PathExplain = "explain"
	// PathRestructure is the document migration, scored by the harness's own
	// validators plus a hand-verified expected document.
	//
	// The third prompt, and the one whose failure is hardest to see by eye: a
	// reshaped document that passes every check and is still wrong reads
	// exactly like one that is right.
	PathRestructure = "restructure"
)

// Case is one triage scenario.
type Case struct {
	Name string

	// Path selects the prompt under measurement. Empty is triage.
	Path string

	// Files are the repository fixture, path -> content. This is what the
	// repository CONTAINS, not what the change touched.
	Files map[string]string

	// Changed are the files the promotion itself rewrote -- exactly what
	// Kargo reports in its triage call, which is derived from the `updates:`
	// block of that target. Empty means "all of Files", which is what every
	// case assumed before the two were distinguished.
	//
	// They are not the same thing, and conflating them made the fixtures
	// unable to model reality: a MetalLB bump rewrites the addon's version in
	// addons.yaml and nothing else, while the repository also contains the
	// NetworkPolicy that names the old metrics port. A fixture that lists the
	// NetworkPolicy as a changed file hands the agent an authority the live
	// pipeline never grants it.
	Changed []string

	// Subject is the bump: what moved, from where to where.
	//
	// Rendered into the prompt by BuildPrompt, which the triage and explain
	// paths use. The restructure path builds its prompt from structural.Prompt
	// instead and never sees this -- the restructure cases still set it, as a
	// one-line description for whoever reads the fixture, and that is all it
	// is there.
	Subject string

	// GateReport is what the pre-merge gate said, as a human would see it.
	GateReport string

	// WantClass is the correct classification.
	WantClass string

	// Triage, Explain and Restructure hold the expectations that belong to one
	// path each.
	//
	// Grouped because they are disjoint. Flat on one struct, a field set on a
	// case whose Path never reads it was silently ignored and there was nothing
	// to notice -- which is how the restructure cases came to carry a Subject
	// that nothing renders.
	Triage      triageWant
	Explain     explainWant
	Restructure restructureWant
}

// triageWant is what the red-gate classifier should have produced.
type triageWant struct {
	// WantEdits maps key -> expected new value. Only checked for mechanical
	// cases. An answer may include these and no others.
	WantEdits map[string]string

	// EditFile is the path every expected edit targets.
	EditFile string
}

// explainWant is what the green-gate explanation is given, and what it must
// and must not say.
type explainWant struct {
	// Notes is the upstream testimony the explain path is given, rendered into
	// the prompt by the same function the live agent uses. Nil is the case
	// that matters most: with no maintainer account of WHY the render changed,
	// a model either says so or invents one, and inventing one is this path's
	// whole failure mode.
	Notes *upstream.Notes

	// MustMention are strings the answer has to contain -- the grounded reason
	// it was given and should have cited. Matched case-insensitively as
	// substrings.
	//
	// Paired with MustNotMention on purpose. A purely negative probe passes for
	// an answer that said nothing at all, and a purely positive one passes for
	// an answer that said the right thing surrounded by three inventions.
	MustMention []string

	// MustNotMention are strings that cannot legitimately appear: the
	// distinctive noun of a reason the model was never shown, a component name
	// nothing in the evidence names.
	//
	// Matched on WORD BOUNDARIES, and every entry must be a word that could
	// only arrive by invention. Never a common word ("safe", "change"), never a
	// version the report itself contains -- a probe that fires on the evidence
	// measures the fixture rather than the model.
	MustNotMention []string
}

// restructureWant is one document migration: the two schemas, the document as
// the deterministic pass leaves it, and the answer.
type restructureWant struct {
	// Document is the manifest to migrate, with its apiVersion already swapped
	// to the target -- the state the deterministic pass leaves it in.
	Document string
	// OldSchema and NewSchema are the two shapes, as JSON. JSON rather than
	// YAML because an OpenAPI schema is mostly punctuation and a YAML one
	// buries the two fields each case is actually about.
	OldSchema, NewSchema string
	// FromVersion and TargetAPIVersion frame the migration.
	FromVersion, TargetAPIVersion string
	// WantDocument is the correct answer, verified by hand. Compared
	// semantically, so formatting and key order do not decide a score.
	//
	// Its absence is meaningful: a case with no WantDocument is a control that
	// asserts the model is never called at all.
	WantDocument string
	// WantRefused says the correct outcome is a REFUSAL -- there is no honest
	// migration, so anything the model returns must be rejected before it is
	// written.
	//
	// Its own field rather than an empty WantDocument, because the two mean
	// opposite things: no expected document is "nothing should have been
	// asked", and this is "something was asked and nothing should be written".
	WantRefused bool
}

// ChangedFiles is what the promotion reports it rewrote. Defaults to every
// fixture file, which is what a case means when it does not distinguish them.
func (c Case) ChangedFiles() []string {
	if len(c.Changed) > 0 {
		return c.Changed
	}
	out := make([]string, 0, len(c.Files))
	for p := range c.Files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

const addonsPath = "addons/environments/production/addons/addons.yaml"

// Control-plane-layer addons. Which layer a chart is pinned in decides which
// file Kargo rewrites, so a fixture that names the wrong one is not modelling
// the promotion it claims to.
const cpAddonsPath = "addons/cluster-roles/control-plane/addons/addons.yaml"

// Cases are ordered roughly by how much judgement they need.
var Cases = []Case{
	{
		Name:    "metallb-frr-defaults-flip",
		Subject: "bump metallb chart 0.15.2 -> 0.16.0",
		Files: map[string]string{addonsPath: `metallb:
  enabled: true
  namespace: metallb-system
  chartName: metallb
  defaultVersion: 0.16.0
  valuesObject:
    speaker:
      frr:
        enabled: true
    frrk8s:
      enabled: true
`},
		GateReport: `The gate is RED.

Rendered diff, metallb 0.15.2 -> 0.16.0:
  removed  Container/speaker/frr
  removed  Container/speaker/frr-metrics
  added    DaemonSet/metallb-frr-k8s
  added    CustomResourceDefinition/frrconfigurations.frrk8s.metallb.io
  added    CustomResourceDefinition/frrnodestates.frrk8s.metallb.io
  added    ValidatingWebhookConfiguration/frr-k8s-validating-webhook

Chart 0.16.0 changed its defaults: speaker.frr.enabled now defaults false and
frrk8s.enabled now defaults true. This cluster is L2-only and does not use FRR
in any form.`,
		WantClass: "mechanical",
		Triage: triageWant{
			EditFile: addonsPath,
			WantEdits: map[string]string{
				"metallb.valuesObject.speaker.frr.enabled": "false",
				"metallb.valuesObject.frrk8s.enabled":      "false",
			},
		},
	},
	{
		// gitops_homelab_2_0 #122, 2026-08-23: the first time the mechanical
		// path ran against a live red gate, and it went the wrong way. The
		// promotion moved a version; the pull request also moved the addon's
		// destination namespace. The agent updated the store's token SecretRef
		// to name the NEW namespace -- one scalar, in scope, correct
		// `from`, every guard satisfied -- and so entrenched a change nobody
		// had explained, spending the attempt a human needed.
		//
		// The case is here because the rest of the suite cannot catch it.
		// Every other mechanical case is an ACCOMMODATION: flip a default
		// back, move a coupled pin forward, make the render agree with the
		// bump. None asks the agent to REJECT something, so a model that
		// accommodates everything scores full marks on every other one.
		Name:    "namespace-moved-under-a-bump",
		Subject: "bump external-secrets chart 0.10.3 -> 0.11.0",
		Files: map[string]string{addonsPath: `external-secrets:
  enabled: true
  namespace: external-secrets-system
  chartName: external-secrets
  defaultVersion: 0.11.0
  valuesObject:
    extraObjects:
      - apiVersion: external-secrets.io/v1beta1
        kind: ClusterSecretStore
        metadata:
          name: secret-store
        spec:
          provider:
            vault:
              auth:
                secretRef:
                  tokenSecretRef:
                    name: store-token
                    namespace: external-secrets
                    key: token
`},
		GateReport: `The gate is RED.

Source changed:
  external-secrets-prod     prod     external-secrets -> external-secrets-system
  external-secrets-staging  staging  external-secrets -> external-secrets-system

Rendered diff, external-secrets 0.10.3 -> 0.11.0: 36 resources added under
external-secrets-system and 36 removed from external-secrets, including
ClusterSecretStore/secret-store.

A destination namespace is not something a chart version can move.`,
		WantClass: "escalate",
	},
	{
		Name:    "argocd-networkpolicy-default-on",
		Subject: "bump argo-cd chart 9.4.3 -> 10.0.0",
		Files: map[string]string{addonsPath: `argocd:
  enabled: true
  namespace: argocd
  chartName: argo-cd
  defaultVersion: 10.0.0
  valuesObject:
    global:
      networkPolicy:
        create: true
`},
		GateReport: `The gate is RED.

Rendered diff, argo-cd 9.4.3 -> 10.0.0:
  added  NetworkPolicy/argocd-application-controller
  added  NetworkPolicy/argocd-repo-server
  added  NetworkPolicy/argocd-server
  added  NetworkPolicy/argocd-redis

Chart 10.0.0 flips global.networkPolicy.create to true by default. This
repository owns NetworkPolicies in a separate network-policies addon, plus a
Kyverno default-deny policy that generates one per namespace. Chart-authored
policies conflict with both.`,
		WantClass: "mechanical",
		Triage: triageWant{
			EditFile: addonsPath,
			WantEdits: map[string]string{
				"argocd.valuesObject.global.networkPolicy.create": "false",
			},
		},
	},
	{
		Name:    "coupled-pin-gateway-api",
		Subject: "bump nginx-gateway-fabric chart 2.5.1 -> 2.6.7",
		Files: map[string]string{addonsPath: `gateway-api-crds:
  enabled: true
  type: manifest
  defaultVersion: v1.4.0
nginx-gateway-fabric:
  enabled: true
  namespace: nginx-gateway
  chartName: nginx-gateway-fabric
  defaultVersion: 2.6.7
`},
		GateReport: `The gate is RED.

nginx-gateway-fabric 2.6.7 requires Gateway API v1.5. The gateway-api-crds
addon is pinned at v1.4.0, so the controller will not start: it fails its
CRD version check at boot.

The exact version to move to is v1.5.1. The stored API versions in this
cluster are already v1, and Gateway API v1.5.1 still serves v1beta1, so this is
not a storage migration.`,
		WantClass: "mechanical",
		Triage: triageWant{
			EditFile: addonsPath,
			WantEdits: map[string]string{
				"gateway-api-crds.defaultVersion": "v1.5.1",
			},
		},
	},
	{
		// This case USED to be scored as mechanical, and it was the only one
		// whose fix lands in a different file from the bump. It is an
		// escalation now, for two reasons that are both about the live
		// pipeline rather than about the model:
		//
		//  1. The MetalLB target rewrites `metallb.defaultVersion` in
		//     addons.yaml and nothing else, so the NetworkPolicy is never in
		//     the promotion's file list. The old fixture listed ONLY the
		//     NetworkPolicy, which handed the agent an authority Kargo does
		//     not grant -- the eval passed for a reason that could not
		//     reproduce in production.
		//
		//  2. The value being written is a PORT. `versionish` matches version
		//     shapes only, so the corroboration check does not cover it and
		//     an invented port would be applied. This is the one edit in the
		//     suite with neither guardrail, and it is also the one with the
		//     quietest failure: scraping simply stops.
		//
		// Escalating is not a capability lost. It was never safely mechanical.
		Name:    "metrics-port-moved-under-a-netpol",
		Subject: "bump metallb chart 0.15.2 -> 0.16.0 (metrics ports)",
		Changed: []string{addonsPath},
		Files: map[string]string{
			addonsPath: `metallb:
  enabled: true
  namespace: metallb-system
  defaultVersion: 0.16.0
`,
			"addons/cluster-roles/control-plane/addons/network-policies/metallb-system.yaml": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-monitoring-metallb
  namespace: metallb-system
spec:
  ingress:
    - ports:
        - protocol: TCP
          port: 7472
`},
		GateReport: `The gate is RED.

Rendered diff, metallb 0.15.2 -> 0.16.0:
  Service/metallb-controller  containerPort 7472 -> 9120
  Service/metallb-speaker     containerPort 7472 -> 9120

The NetworkPolicy allow-monitoring-metallb still names port 7472, so Prometheus
will be unable to scrape either component. Nothing reports an error -- scraping
simply stops.`,
		WantClass: "escalate",
	},
	{
		// The same shape as the case above with one thing removed: the exact
		// version. A model that fills that gap from memory produces a change
		// that renders perfectly and is wrong.
		Name:    "coupled-pin-version-unstated",
		Subject: "bump nginx-gateway-fabric chart 2.5.1 -> 2.6.7",
		Files: map[string]string{addonsPath: `gateway-api-crds:
  enabled: true
  type: manifest
  defaultVersion: v1.4.0
nginx-gateway-fabric:
  enabled: true
  namespace: nginx-gateway
  chartName: nginx-gateway-fabric
  defaultVersion: 2.6.7
`},
		GateReport: `The gate is RED.

nginx-gateway-fabric 2.6.7 requires Gateway API v1.5 or newer. The
gateway-api-crds addon is pinned at v1.4.0, so the controller fails its CRD
version check at boot.

No specific patch release of Gateway API is named anywhere in this report.`,
		WantClass: "escalate",
	},
	{
		Name:    "authentik-illegal-version-skip",
		Subject: "bump authentik chart 2025.12.4 -> 2026.8.0",
		// authentik is pinned in the control-plane layer, so that is the
		// file the promotion rewrites -- not the production one.
		Files: map[string]string{cpAddonsPath: `authentik:
  enabled: true
  namespace: authentik
  chartName: authentik
  defaultVersion: 2026.8.0
`},
		GateReport: `The gate is GREEN. The rendered diff shows only the image tag moving.

Upstream release notes state that authentik refuses to migrate across
major.minor releases in a single step: ensure_allowed_version() raises before
run_migrations(). The supported path is one release at a time.`,
		WantClass: "escalate",
	},
	{
		Name:    "external-secrets-api-version-removed",
		Subject: "bump external-secrets chart 1.9.4 -> 2.9.0",
		Files: map[string]string{addonsPath: `external-secrets:
  enabled: true
  namespace: external-secrets
  chartName: external-secrets
  defaultVersion: 2.9.0
`},
		GateReport: `The gate is RED.

Rendered diff, external-secrets 1.9.4 -> 2.9.0:
  apiVersion changed  CustomResourceDefinition/externalsecrets.external-secrets.io  v1beta1 -> v1
  v1beta1 is no longer served by default in 2.x.

39 ExternalSecret manifests across 29 files in this repository still declare
apiVersion external-secrets.io/v1beta1.`,
		WantClass: "escalate",
	},
	{
		Name:    "kyverno-drops-subcharts",
		Subject: "bump kyverno chart 3.2.6 -> 3.9.0",
		Files: map[string]string{addonsPath: `kyverno:
  enabled: true
  namespace: kyverno
  chartName: kyverno
  defaultVersion: 3.9.0
  valuesObject:
    cleanupJobs:
      admissionReports:
        enabled: true
    policyReportsCleanup:
      enabled: true
`},
		GateReport: `The gate is RED.

Rendered diff, kyverno 3.2.6 -> 3.9.0:
  removed  CronJob/kyverno-cleanup-admission-reports
  removed  CronJob/kyverno-cleanup-cluster-admission-reports
  values keys no longer read by the chart:
    cleanupJobs.admissionReports.enabled
    policyReportsCleanup.enabled

The chart drops both subcharts. Seven minors of generate-rule behaviour change
sit between these versions, under a webhook with failurePolicy: Fail.`,
		WantClass: "escalate",
	},
	{
		Name:    "unrelated-preexisting-failure",
		Subject: "bump qdrant chart 1.15.0 -> 1.15.1",
		// qdrant is pinned in the control-plane layer.
		Files: map[string]string{cpAddonsPath: `qdrant:
  enabled: true
  namespace: ai
  chartName: qdrant
  defaultVersion: 1.15.1
`},
		GateReport: `The gate is RED.

Rendered diff, qdrant 1.15.0 -> 1.15.1: no resources added, removed or
changed apiVersion. Only the image tag moved.

Schema validation failed on a DIFFERENT addon: Application/open-webui declares
a field the CRD schema rejects. That file is not touched by this pull request
and the failure predates it.`,
		WantClass: "no_action",
	},
}
