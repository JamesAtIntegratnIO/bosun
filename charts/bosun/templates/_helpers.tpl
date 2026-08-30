{{- define "bosun.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bosun.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "bosun.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "bosun.labels" -}}
app.kubernetes.io/name: {{ include "bosun.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: delivery-kit
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "bosun.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bosun.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "bosun.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "bosun.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* Digest wins over tag: a moving tag is how an unpinned image silently
     changes underneath you, which is the class of incident this whole kit
     exists to prevent. */}}
{{- define "bosun.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}
{{- end -}}

{{/* Fail early and by name, rather than rendering something that cannot run. */}}
{{- define "bosun.validate" -}}
{{- if not .Values.image.repository }}{{ fail "bosun: image.repository is required" }}{{ end -}}
{{- if not .Values.git.owner }}{{ fail "bosun: git.owner is required" }}{{ end -}}
{{- if not .Values.git.repo }}{{ fail "bosun: git.repo is required" }}{{ end -}}
{{- if not .Values.git.repoURL }}{{ fail "bosun: git.repoURL is required" }}{{ end -}}
{{- if not .Values.git.existingSecret }}{{ fail "bosun: git.existingSecret is required; this chart never creates a Secret" }}{{ end -}}
{{- if not .Values.llm.provider }}{{ fail "bosun: llm.provider is required (openai or anthropic); there is deliberately no default" }}{{ end -}}
{{- if not .Values.llm.model }}{{ fail "bosun: llm.model is required" }}{{ end -}}
{{- if and (eq .Values.llm.provider "openai") (not .Values.llm.baseURL) }}{{ fail "bosun: llm.baseURL is required for the openai provider" }}{{ end -}}
{{- if not .Values.triage.allowPaths }}{{ fail "bosun: triage.allowPaths is empty, so the agent could never apply a fix" }}{{ end -}}
{{/* The scrape's ingress rule is a pod label and a namespace, and a pod label
     on its own is chosen by whoever creates the pod. Without the namespace,
     enabling the ServiceMonitor opens the service's whole HTTP surface to
     anything in the cluster that labels itself prometheus, which is wider than
     the rule it sits beside and reads exactly like it is not. Refused rather
     than rendered, because nothing about the running install would show it. */}}
{{- if and .Values.metrics.serviceMonitor.enabled .Values.networkPolicy.enabled (not .Values.metrics.serviceMonitor.namespace) }}{{ fail "bosun: metrics.serviceMonitor.namespace is required when the ServiceMonitor and the NetworkPolicy are both enabled; without it the scrape rule admits any pod labelled prometheus, in any namespace" }}{{ end -}}
{{/* A route to a port nothing may reach. The Ingress or HTTPRoute renders,
     the Gateway accepts it, the page answers nothing, and the symptom is a
     timeout at the gateway that points at the gateway. Refused here, where the
     missing value has a name. */}}
{{- if and .Values.web.enabled (or .Values.web.httpRoute.enabled .Values.web.ingress.enabled) .Values.networkPolicy.enabled (not .Values.web.allowFrom) }}{{ fail "bosun: web.allowFrom is empty while the status page is published and the NetworkPolicy is on, so nothing may reach the page's port; name your gateway's namespace, e.g. [{namespace: gateway-system}]" }}{{ end -}}
{{/* The MCP surface with nothing to authenticate with.

     The binary already refuses to start that listener and says why at every
     start-up, which is the right behaviour for a hand-edited Deployment. It is
     the wrong place to find out from a values file: the pod runs, the Service
     publishes a port, and the only symptom is one WARNING in a log nobody is
     reading. Refused here, at install time, where the missing value has a name.

     `dangerouslyServeWithoutAuthentication` is the way past it, and it is
     spelled to be uncomfortable to type on purpose. */}}
{{- if and .Values.mcp.enabled (not .Values.mcp.existingSecret) (not .Values.mcp.dangerouslyServeWithoutAuthentication) }}{{ fail "bosun: mcp.existingSecret is required when mcp.enabled is true; without a token the MCP listener does not start at all. This chart never creates a Secret -- mint a token and name the Secret holding it. If you genuinely want an unauthenticated read API on that port, say so with mcp.dangerouslyServeWithoutAuthentication" }}{{ end -}}
{{/* The MCP surface published to nobody. Same shape as web.allowFrom above,
     and it matters more here: this is the listener built to be reached from
     outside the cluster, so a policy that admits nothing is a surface that
     answers nothing, from a port that looks published. */}}
{{- if and .Values.mcp.enabled .Values.networkPolicy.enabled (not .Values.mcp.allowFrom) }}{{ fail "bosun: mcp.allowFrom is empty while the MCP surface is enabled and the NetworkPolicy is on, so nothing may reach its port; name the namespace your client or gateway runs in, e.g. [{namespace: gateway-system}]" }}{{ end -}}
{{/* A route with the page switched off publishes a port the pod does not
     listen on, and neither the Service nor the route says so. */}}
{{- if and (not .Values.web.enabled) (or .Values.web.httpRoute.enabled .Values.web.ingress.enabled) }}{{ fail "bosun: web.httpRoute or web.ingress is enabled while web.enabled is false; there would be nothing listening behind the route" }}{{ end -}}
{{- end -}}
