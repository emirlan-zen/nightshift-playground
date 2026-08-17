.PHONY: init plan apply destroy fmt output ssh logs web control control-bin control-test dev deploy deploy-prompts

TF := terraform -chdir=terraform
DEVHOME := $(CURDIR)/control/.devhome
AGENT_HOME := /home/agent

web:
	pnpm -C control/web install
	pnpm -C control/web build

control: web
	go build -C control -o control .

control-bin:
	go build -C control -o control .

control-test:
	go vet -C control ./...
	go test -C control ./...

dev: control-bin
	@mkdir -p $(DEVHOME)
	@echo "dev: control :8787 + vite :5173 — Ctrl-C to stop"
	@HOME=$(DEVHOME) NIGHTSHIFT_DEV=1 LISTEN=127.0.0.1:8787 ./control/control & \
	  backend_pid=$$!; trap "kill $$backend_pid 2>/dev/null" EXIT INT TERM; \
	  pnpm -C control/web install && pnpm -C control/web dev

init:
	$(TF) init

fmt:
	$(TF) fmt -recursive

plan:
	$(TF) plan

apply:
	$(TF) apply

destroy:
	$(TF) destroy

output:
	$(TF) output

ssh:
	ssh root@$$($(TF) output -raw devbox_ipv4)

logs:
	ssh root@$$($(TF) output -raw devbox_ipv4) 'tail -f /var/log/nightshift-install.log'

# Assumes the VM's /opt/nightshift remote is already authenticated.
deploy:
	ssh root@$$($(TF) output -raw devbox_ipv4) \
	  'git -C /opt/nightshift pull --ff-only && bash /opt/nightshift/bootstrap/install.sh'

deploy-prompts:
	tar -C files -cf - nightrun | ssh root@$$($(TF) output -raw devbox_ipv4) 'set -e; \
	  work=$$(mktemp -d); trap "rm -rf $$work" EXIT; tar -C $$work -xf -; \
	  for f in $$work/nightrun/sweep/*.md; do \
	    install -o agent -g agent -m 0644 "$$f" "$(AGENT_HOME)/.nightshift/sweep/$$(basename "$$f")"; done; \
	  install -o agent -g agent -m 0644 $$work/nightrun/contract.md $(AGENT_HOME)/.nightshift/contract.md; \
	  install -m0755 $$work/nightrun/nightshift-run-launcher /usr/local/bin/nightshift-run-launcher; \
	  install -m0755 $$work/nightrun/claude-auth-probe /usr/local/bin/claude-auth-probe; \
	  install -m0755 $$work/nightrun/forge-auth-probe /usr/local/bin/forge-auth-probe; \
	  install -m0755 $$work/nightrun/codex-auth-probe /usr/local/bin/codex-auth-probe'
