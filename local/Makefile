# The local proving ground. See README.md.
#
# LLM_BASE_URL has no default on purpose: the agent talks to a model endpoint
# and guessing one for you is how a demo quietly starts spending money.

SHELL := /usr/bin/env bash
SCRIPTS := scripts

.PHONY: help
help:
	@echo "make up      colima + idpbuilder + platform + kit + seed  (everything)"
	@echo "make demo    run the whole flow, asserted at every step"
	@echo "make demo-triage  a PR the gate REFUSES, and the agent's verdict on it"
	@echo "make scenarios    replay the ten recorded incidents against the live agent"
	@echo "make demo-structural  a bump the swap cannot fix, and the model that can"
	@echo "make demo-forged  a gate report the agent refuses to believe"
	@echo "make demo-egress  a host the agent is told not to visit"
	@echo "make reset   put the flow back to the start so demo can run again"
	@echo "make down    delete the cluster; colima keeps running"
	@echo "make clean   down, and stop colima too"
	@echo
	@echo "required:  LLM_BASE_URL=http://<host>:<port>/v1"
	@echo "optional:  LLM_MODEL (default qwen/qwen3.5-9b)"

.PHONY: up
up: runtime cluster platform seed kit
	@echo "ready -- now run: make demo"

.PHONY: runtime
runtime:
	@bash $(SCRIPTS)/00-runtime.sh

.PHONY: cluster
cluster:
	@bash $(SCRIPTS)/05-cluster.sh

.PHONY: platform
platform:
	@bash $(SCRIPTS)/10-platform.sh

.PHONY: seed
seed:
	@bash $(SCRIPTS)/20-seed.sh

.PHONY: kit
kit:
	@bash $(SCRIPTS)/30-kit.sh

.PHONY: demo
demo:
	@bash $(SCRIPTS)/40-demo.sh

# The demo consumes the Freight it promotes, so a second run has nothing left
# to do. This puts BOTH sides back -- the repository and Kargo's stage state.
# Act two: a pull request the gate refuses, and an agent that has to answer
# for it. Run after `make demo`.
.PHONY: demo-triage
demo-triage:
	@bash $(SCRIPTS)/50-demo-triage.sh

# The eval suite's cases, replayed as real pull requests against the live
# in-cluster agent. The gate report each posts is the recorded one; the agent,
# the model and every commit it pushes are live. Referenced by both READMEs
# since the day the script landed -- and wired to a target only after a reader
# followed the instruction and found nothing there.
.PHONY: scenarios
scenarios:
	@bash $(SCRIPTS)/60-demo-scenarios.sh

# Act three: the bump the deterministic repair cannot finish, where the model
# writes the migration and three checks decide whether it is allowed to.
# Wired to a target the day the script landed, because the last script that was
# not spent a week being referenced by two READMEs and reachable from neither.
.PHONY: demo-structural
demo-structural:
	@bash $(SCRIPTS)/70-demo-structural.sh

# Act four: a report carrying the gate's marker, written by an account that is
# not the gate. Asserts the refusal AND that it says whose report it ignored --
# a silent refusal is indistinguishable from a crash.
.PHONY: demo-forged
demo-forged:
	@bash $(SCRIPTS)/80-demo-forged-report.sh

# Act five: the deny half of "egress is open and logged". Forbids a host the
# agent is about to need, proves the refusal names its rule, and proves the
# triage still reaches a verdict without it. Changes the running deployment and
# puts it back, including on failure.
.PHONY: demo-egress
demo-egress:
	@bash $(SCRIPTS)/85-demo-egress.sh


.PHONY: reset
reset:
	@bash $(SCRIPTS)/95-reset.sh

.PHONY: down
down:
	@bash $(SCRIPTS)/90-down.sh

.PHONY: clean
clean: down
	@colima stop
