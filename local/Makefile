# Bosun's local proving ground. See README.md.
#
# LLM_BASE_URL has no default on purpose: Bosun talks to a model endpoint and
# guessing one for you is how a demo quietly starts spending money.

SHELL := /usr/bin/env bash
SCRIPTS := scripts

.PHONY: help
help:
	@echo "make up         colima + idpbuilder + seed + bosun  (everything)"
	@echo "make scenarios  9 real incidents replayed against the live agent"
	@echo "                CASE=<substring> runs one"
	@echo "make down       delete the cluster; colima keeps running"
	@echo "make clean      down, and stop colima too"
	@echo
	@echo "required:  LLM_BASE_URL=http://<host>:<port>/v1"
	@echo "optional:  LLM_MODEL (default qwen/qwen3.5-9b)"

.PHONY: up
up: runtime cluster seed agent
	@echo "ready -- now run: make scenarios"

.PHONY: runtime
runtime:
	@bash $(SCRIPTS)/00-runtime.sh

.PHONY: cluster
cluster:
	@bash $(SCRIPTS)/05-cluster.sh

.PHONY: seed
seed:
	@bash $(SCRIPTS)/20-seed.sh

# Rebuilds the image from the working tree and rolls the deployment onto it.
# Run this after every code change -- the tag does not move, so helm alone
# would keep the old binary running.
.PHONY: agent
agent:
	@bash $(SCRIPTS)/30-agent.sh

.PHONY: scenarios
scenarios:
	@bash $(SCRIPTS)/40-scenarios.sh $(CASE)

.PHONY: down
down:
	@bash $(SCRIPTS)/90-down.sh

.PHONY: clean
clean: down
	@colima stop
