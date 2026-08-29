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
{{- end -}}
