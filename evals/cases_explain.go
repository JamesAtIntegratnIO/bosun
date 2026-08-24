package evals

import "github.com/JamesAtIntegratnIO/bosun/upstream"

// The explain path's cases live in their own file because they are measured
// against a different prompt and score on a different thing, and mixing them
// into the triage list would hide both facts behind an ordering.
//
// They are appended to Cases rather than kept apart so that there is still ONE
// list. The live scenario demo reads that list; a second export it did not know
// about is how the thing the suite measures and the thing anyone watches start
// to drift.
func init() { Cases = append(Cases, explainCases...) }

// What this path can get wrong, and why nothing downstream catches it.
//
// The triage prompt's failure lands on disk, where an applier is standing in
// front of it: a wrong path is refused, a wrong `from` is refused, an invented
// version is refused. The explain prompt writes nothing. Its failure is a
// fluent, plausible account of what a version "did", assembled from what the
// model remembers about the project rather than from the two sources it was
// handed -- and it goes straight to somebody about to press merge, who cannot
// tell it from the half that was true.
//
// So the pairs matter more than the cases. The same removed ClusterRole
// appears twice, once with a maintainer's explanation and once without, and the
// interesting number is whether the second answer still contains the first
// answer's reason.
var explainCases = []Case{
	{
		// The shape of a bump that is not a bump. A green gate is a verdict on
		// the render, and the render of a chart that crossed a major boundary
		// can be perfectly ordinary right up until the software refuses to
		// start on a schema it has not been migrated through.
		Name:    "explain-a-major-boundary-is-not-a-bump",
		Path:    PathExplain,
		Subject: "bump external-secrets chart 0.9.20 -> 0.11.0",
		Files: map[string]string{addonsPath: `external-secrets:
  enabled: true
  namespace: external-secrets-system
  chartName: external-secrets
  defaultVersion: 0.11.0
`},
		GateReport: `<!-- gitops-gate -->
The gate is GREEN. Nothing structural changed: no Application moved between
clusters, no source changed, no apiVersion was migrated.

Rendered diff, external-secrets 0.9.20 -> 0.11.0:
  changed  Deployment/external-secrets   (image tag, 3 arg flags)
  changed  Deployment/external-secrets-webhook   (image tag)
  added    ServiceMonitor/external-secrets-metrics
  changed  CustomResourceDefinition/externalsecrets.external-secrets.io
  changed  CustomResourceDefinition/clustersecretstores.external-secrets.io

No manifest in this repository declares a version that stopped being served.`,
		WantClass: "escalate",
		// The distance is the finding, and it is written in the report. An
		// answer that does not name where the chart came from and where it is
		// going has not made the point it is escalating on.
		MustMention: []string{"0.9.20", "0.11.0"},
		// Nothing in front of it says anything about data loss, a migration
		// job, or a controller rewrite. Those are the sentences a model
		// produces from memory of the project.
		MustNotMention: []string{"backup", "irreversible", "downtime"},
	},
	{
		// A resource disappearing is the finding a render can prove and cannot
		// explain. With the maintainers' own words in front of it, the answer
		// should say WHY -- the whole reason upstream notes are fetched.
		Name:    "explain-removed-rbac-with-the-reason-in-front-of-it",
		Path:    PathExplain,
		Subject: "bump trivy-operator-explorer chart 0.5.8 -> 1.0.0",
		Files: map[string]string{addonsPath: `trivy-explorer:
  enabled: true
  namespace: security
  chartName: trivy-operator-explorer
  defaultVersion: 1.0.0
`},
		GateReport: `<!-- gitops-gate -->
The gate is GREEN. Nothing structural changed.

Rendered diff, trivy-operator-explorer 0.5.8 -> 1.0.0:
  removed  ClusterRole/trivy-operator-explorer
  removed  ClusterRoleBinding/trivy-operator-explorer
  added    Role/trivy-operator-explorer
  added    RoleBinding/trivy-operator-explorer
  changed  Deployment/trivy-operator-explorer   (image tag, 1 env var)`,
		WantClass: "escalate",
		Notes: &upstream.Notes{
			SourceRepo: "example-org/trivy-operator-explorer",
			Releases: []upstream.Release{{
				Tag:  "v1.0.0",
				Name: "1.0.0",
				Body: "### Breaking\n\n" +
					"The explorer no longer reads reports cluster-wide. It watches the " +
					"namespaces listed in the new `targetNamespaces` value and ships a " +
					"namespaced Role instead of a ClusterRole. Deployments that relied on " +
					"cluster-wide discovery must list their namespaces explicitly.\n",
			}},
		},
		// The reason was handed to it. Citing it is the job.
		MustMention: []string{"targetNamespaces"},
	},
	{
		// The same finding with the notes taken away. This is where invention
		// lives: the render still shows a ClusterRole disappearing, and the
		// model still knows -- or believes it knows -- why.
		//
		// The probe is the previous case's reason. `targetNamespaces` is a real
		// upstream value name, it is nowhere in this case's evidence, and there
		// is exactly one way for it to appear in the answer.
		Name:    "explain-removed-rbac-with-no-reason-given",
		Path:    PathExplain,
		Subject: "bump trivy-operator-explorer chart 0.5.8 -> 1.0.0",
		Files: map[string]string{addonsPath: `trivy-explorer:
  enabled: true
  namespace: security
  chartName: trivy-operator-explorer
  defaultVersion: 1.0.0
`},
		GateReport: `<!-- gitops-gate -->
The gate is GREEN. Nothing structural changed.

Rendered diff, trivy-operator-explorer 0.5.8 -> 1.0.0:
  removed  ClusterRole/trivy-operator-explorer
  removed  ClusterRoleBinding/trivy-operator-explorer
  added    Role/trivy-operator-explorer
  added    RoleBinding/trivy-operator-explorer
  changed  Deployment/trivy-operator-explorer   (image tag, 1 env var)`,
		WantClass: "escalate",
		Notes: &upstream.Notes{
			Note: "No upstream release notes: the artifact carries no source label.",
		},
		// Say what the render did. That much is fact and it is the finding.
		MustMention: []string{"ClusterRole"},
		// Say why, and it came from somewhere that is not the evidence.
		MustNotMention: []string{"targetNamespaces", "namespaced"},
	},
	{
		// The over-flagging guard, and the reason it is here: an escalation
		// that fires on a routine bump costs more than it was ever worth,
		// because the next one is not read. Every other explain case rewards
		// noticing something.
		Name:    "explain-a-routine-bump-stays-quiet",
		Path:    PathExplain,
		Subject: "bump authentik chart 2026.6.3 -> 2026.6.4",
		Files: map[string]string{addonsPath: `authentik:
  enabled: true
  namespace: authentik
  chartName: authentik
  defaultVersion: 2026.6.4
`},
		GateReport: `<!-- gitops-gate -->
The gate is GREEN. Nothing structural changed.

Rendered diff, authentik 2026.6.3 -> 2026.6.4:
  changed  Deployment/authentik-server    (image tag)
  changed  Deployment/authentik-worker    (image tag)
  changed  StatefulSet/authentik-redis    (one label: app.kubernetes.io/version)`,
		WantClass: "no_action",
		Notes: &upstream.Notes{
			SourceRepo: "goauthentik/authentik",
			Releases: []upstream.Release{{
				Tag: "version/2026.6.4", Name: "2026.6.4",
				Body: "Patch release. Fixes a flow-inspector rendering error and updates " +
					"the bundled frontend dependencies.\n",
			}},
		},
		// Nothing in either source mentions a vulnerability, and "there might
		// be a security fix in here" is the most reliable way this path turns
		// into noise.
		MustNotMention: []string{"CVE", "vulnerability", "exploit"},
	},
	{
		// A chart that stops shipping a CRD renders green when nothing in the
		// repository declares one -- the gate has counted, and says so. That is
		// exactly the promotion whose diff is one version number and whose
		// consequence arrives later, when something that was using the CRD
		// through some other path finds it gone.
		Name:    "explain-a-crd-that-stopped-shipping",
		Path:    PathExplain,
		Subject: "bump kyverno chart 3.5.2 -> 3.6.0",
		Files: map[string]string{cpAddonsPath: `kyverno:
  enabled: true
  namespace: kyverno
  chartName: kyverno
  defaultVersion: 3.6.0
`},
		GateReport: `<!-- gitops-gate -->
The gate is GREEN. Nothing structural changed.

Rendered diff, kyverno 3.5.2 -> 3.6.0:
  removed  CustomResourceDefinition/policyexceptions.kyverno.io
  removed  CustomResourceDefinition/cleanuppolicies.kyverno.io
  added    PodDisruptionBudget/kyverno-admission-controller
  changed  Deployment/kyverno-admission-controller   (image tag)

Consumers of the removed CustomResourceDefinitions in this repository: 0.`,
		WantClass: "escalate",
		Notes: &upstream.Notes{
			Note: "No upstream release notes: no release in this range carried a body.",
		},
		// The removal is the finding, and it has a name in the report.
		MustMention: []string{"policyexceptions"},
		// Zero consumers is what the report counted. Reading that as "nothing
		// anywhere uses these" is a claim about the cluster, which this path
		// has no evidence about at all.
		MustNotMention: []string{"unused", "harmless"},
	},
}
