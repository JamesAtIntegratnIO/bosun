#!/usr/bin/env bash
# Waits for the platform ArgoCD is installing from the sample repository.
#
# This used to be 10-platform.sh, a hundred and twenty lines of
# `helm upgrade --install` run before the repository existed. The argument for
# it was that these are "the platform the thing under test runs on", the
# equivalent of what idpbuilder itself installed, and a reconcile loop only
# added a minute per run.
#
# That was wrong twice. This repository's pattern is app-of-apps, and a proving
# ground that installs its own platform by hand is not proving the pattern it
# exists to demonstrate. The definitions live in the sample repository now --
# `platform/` -- where they are visible, diffable, and reconciled by the same
# ArgoCD the demo watches. This script only waits.
#
# It runs AFTER the seed, and its number says so. The platform cannot be
# installed before the repository that defines it exists.
#
# What is still installed imperatively, and why: bosun and kargo-pipelines are
# built FROM THE WORKING TREE, and there is no git ref for ArgoCD to point at.
# That is the entire point of a proving ground -- it exercises the code in
# front of you, not whatever was last published -- so those two stay in
# 30-kit.sh and are the only things here that are not GitOps.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

APPS=(cert-manager argo-rollouts monitoring kargo)

say "the platform, reconciled from ${SAMPLE_REPO_NAME}/platform"
kc -n argocd get application sample-platform >/dev/null 2>&1 \
  || bad "no sample-platform root Application -- run make seed first"

# Nudge rather than wait for the poll. A fresh root Application otherwise sits
# until ArgoCD's next reconcile, which turns a two-minute step into five for
# no reason.
kc -n argocd patch application sample-platform --type merge \
  -p '{"metadata":{"annotations":{"argocd.argoproj.io/refresh":"hard"}}}' >/dev/null 2>&1 || true

# Wave 0 (cert-manager, argo-rollouts, monitoring) must reach Healthy before
# ArgoCD touches wave 1 -- Kargo's webhooks are issued by cert-manager, and
# applied together the webhook Deployment comes up before anything can sign for
# it. The failure names the certificate, not the ordering, which is why this
# waits on each Application by name rather than on the root going green.
for app in "${APPS[@]}"; do
  wait_for "$app exists" 300 kc -n argocd get application "$app"
  wait_for "$app is Healthy and Synced" 900 bash -c \
    "kubectl --context '$CLUSTER_CONTEXT' -n argocd get application $app \
       -o jsonpath='{.status.health.status}/{.status.sync.status}' | grep -qx 'Healthy/Synced'"
done

# Asserted rather than assumed: without it every AnalysisRun fails with an
# empty message. Created by the monitoring chart itself, from
# `prometheus.additionalServiceMonitors`, so it carries the release prefix.
wait_for "argocd is being scraped" 300 bash -c \
  "kubectl --context '$CLUSTER_CONTEXT' -n monitoring get servicemonitor \
     -o name | grep -q argocd-metrics"

say "platform ready"
kc -n argocd get applications --no-headers | sed 's/^/  /'
