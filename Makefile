# custos: build, test and install (also used by the OBS spec).
VERSION    ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
PREFIX     ?= /usr
LIBEXECDIR ?= $(PREFIX)/libexec
DATADIR    ?= $(PREFIX)/share
SBINDIR    ?= $(PREFIX)/sbin
UNITDIR    ?= $(PREFIX)/lib/systemd/system
SYSUSERSDIR ?= $(PREFIX)/lib/sysusers.d
TMPFILESDIR ?= $(PREFIX)/lib/tmpfiles.d
SYSCONFDIR ?= /etc
SELINUXDIR ?= $(DATADIR)/selinux/packages/targeted
COCKPITDIR ?= $(DATADIR)/cockpit/custos
DOCDIR     ?= $(DATADIR)/custos/docs
MCPDOCSDIR ?= $(DATADIR)/mcp-docs/collections.d
SELINUX_DEVEL ?= /usr/share/selinux/devel/Makefile
GO         ?= go
OPA        ?= opa
# The gateway's shipped policy, for testing the agents module against it.
GATEWAY_POLICY ?= $(DATADIR)/mcp-gateway/policy

LDFLAGS := -X github.com/sdrahn/custos/internal/launcher.Version=$(VERSION)
BINS := agent-launcher agent-admin agent-delegate agent-openshell-relay custos-setup
# The functional tests' helpers (package custos-tests).
TESTBINS := fakemodel testsrv testidp secondhost

.PHONY: all build build-tests test check fmt-check vet policy-test cockpit-test selinux install install-selinux install-tests install-cockpit install-doc clean

all: build

build: $(BINS:%=bin/%)

bin/%: FORCE
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $@ ./cmd/$*

build-tests: $(TESTBINS:%=bin/tests/%)

bin/tests/%: FORCE
	$(GO) build -trimpath -o $@ ./test/vm/$*

FORCE:

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt-check:
	@test -z "$$(gofmt -l cmd internal test)" || { gofmt -l cmd internal test; exit 1; }

# The autonomy modes, tested together with the gateway's own policy
# (GATEWAY_POLICY: a checkout's policy/ directory, or the installed one).
policy-test:
	$(OPA) fmt --list --fail gateway/policy
	$(OPA) check --strict $(GATEWAY_POLICY) gateway/policy
	$(OPA) test $(GATEWAY_POLICY) gateway/policy

check: fmt-check vet test

# The Cockpit page against a stub of cockpit.js, in a headless browser
# (needs node and playwright-core or playwright; CHROME=path if not
# Playwright's own Chromium).
cockpit-test:
	for f in cockpit/custos/*.js cockpit/test/*.js test/cockpit/*.js test/video/*.js; do node --check "$$f" || exit 1; done
	node cockpit/test/smoke.js
	node test/video/subtitles_test.js

# Needs selinux-policy-devel and mcp_gateway.if (mcp-gateway-selinux).
selinux: selinux/custos.pp

selinux/custos.pp: selinux/custos.te selinux/custos.fc
	$(MAKE) -C selinux -f $(SELINUX_DEVEL) custos.pp

install: build
	install -d $(DESTDIR)$(LIBEXECDIR)/custos
	install -m 0755 bin/agent-launcher bin/agent-delegate bin/agent-openshell-relay $(DESTDIR)$(LIBEXECDIR)/custos/
	install -d $(DESTDIR)$(SBINDIR)
	install -m 0755 bin/agent-admin $(DESTDIR)$(SBINDIR)/agent-admin
	install -m 0755 bin/custos-setup $(DESTDIR)$(SBINDIR)/custos-setup
	ln -sf ../../sbin/agent-admin $(DESTDIR)$(LIBEXECDIR)/custos/agent-admin
	install -d $(DESTDIR)$(UNITDIR)
	install -m 0644 systemd/agent@.service systemd/agent@.socket systemd/agent-schedule@.service systemd/agent-schedule@.timer systemd/agent-event@.service \
		systemd/agent-openshell@.service systemd/agent-openshell@.socket \
		systemd/custos-demote.service systemd/custos-demote.timer $(DESTDIR)$(UNITDIR)/
	install -m 0755 packaging/openshell/agent-openshell-logs packaging/openshell/agent-openshell-start $(DESTDIR)$(LIBEXECDIR)/custos/
	install -D -m 0644 packaging/openshell/Containerfile $(DESTDIR)$(DATADIR)/custos/openshell/Containerfile
	install -D -m 0644 packaging/sysusers/custos.conf $(DESTDIR)$(SYSUSERSDIR)/custos.conf
	install -D -m 0644 packaging/tmpfiles/custos.conf $(DESTDIR)$(TMPFILESDIR)/custos.conf
	install -D -m 0644 gateway/policy/agents.rego $(DESTDIR)$(DATADIR)/mcp-gateway/policy/custos/agents.rego
	install -D -m 0644 gateway/policy/owners.rego $(DESTDIR)$(DATADIR)/mcp-gateway/policy/custos/owners.rego
	install -D -m 0644 gateway/policy/_control.yaml $(DESTDIR)$(SYSCONFDIR)/mcp-gateway/policy/agents/_control.yaml
	install -D -m 0644 gateway/policy/_http.yaml $(DESTDIR)$(SYSCONFDIR)/mcp-gateway/policy/agents/_http.yaml
	install -D -m 0644 gateway/servers.d/agents.yaml $(DESTDIR)$(DATADIR)/custos/gateway/servers.d/agents.yaml
	install -D -m 0644 gateway/servers.d/custos-net.yaml $(DESTDIR)$(DATADIR)/custos/gateway/servers.d/custos-net.yaml
	install -D -m 0644 gateway/custos-net.d/network.yaml $(DESTDIR)$(DATADIR)/custos/gateway/custos-net.d/network.yaml
	install -D -m 0644 examples/patch-pilot/agent.yaml $(DESTDIR)$(DATADIR)/custos/examples/patch-pilot/agent.yaml
	install -D -m 0644 examples/patch-pilot/permissions.yaml $(DESTDIR)$(DATADIR)/custos/examples/patch-pilot/permissions.yaml
	install -d -m 0755 $(DESTDIR)$(SYSCONFDIR)/custos/agents
	install -d -m 0755 $(DESTDIR)$(DATADIR)/custos/categories.d $(DESTDIR)$(SYSCONFDIR)/custos/categories.d
	install -m 0644 packaging/categories/*.yaml $(DESTDIR)$(DATADIR)/custos/categories.d/
	install -d -m 0755 $(DESTDIR)$(SYSCONFDIR)/custos/templates
	install -D -m 0644 templates/exec.d-disk-usage.yaml $(DESTDIR)$(DATADIR)/custos/templates/exec.d-disk-usage.yaml
	install -D -m 0644 templates/exec.d-diagnostics.yaml $(DESTDIR)$(DATADIR)/custos/templates/exec.d-diagnostics.yaml
	install -d -m 0755 $(DESTDIR)$(DATADIR)/custos/playbooks $(DESTDIR)$(SYSCONFDIR)/custos/playbooks
	install -m 0644 packaging/playbooks/*.yaml $(DESTDIR)$(DATADIR)/custos/playbooks/
	for t in templates/*/; do \
		t=$${t%/}; \
		install -D -m 0644 -t $(DESTDIR)$(DATADIR)/custos/$$t $$t/*.yaml || exit 1; \
		if [ -d $$t/envelopes ]; then install -D -m 0644 -t $(DESTDIR)$(DATADIR)/custos/$$t/envelopes $$t/envelopes/*.yaml || exit 1; fi; \
	done

# The functional tests, for openQA (test/openqa/README.md).
install-tests: build-tests
	install -Dm0755 test/vm/run-tests.sh $(DESTDIR)$(LIBEXECDIR)/custos/tests/run-tests.sh
	install -Dm0644 test/vm/lib.sh $(DESTDIR)$(LIBEXECDIR)/custos/tests/lib.sh
	install -D -m 0644 -t $(DESTDIR)$(LIBEXECDIR)/custos/tests/sections test/vm/sections/*.sh
	install -Dm0755 test/vm/model-eval.sh $(DESTDIR)$(LIBEXECDIR)/custos/tests/model-eval.sh
	install -Dm0755 bin/tests/fakemodel $(DESTDIR)$(LIBEXECDIR)/custos/tests/fakemodel
	install -Dm0755 bin/tests/testsrv $(DESTDIR)$(LIBEXECDIR)/custos/tests/testsrv
	install -Dm0755 bin/tests/testidp $(DESTDIR)$(LIBEXECDIR)/custos/tests/testidp
	install -Dm0755 bin/tests/secondhost $(DESTDIR)$(LIBEXECDIR)/custos/tests/secondhost

# The Cockpit page (package custos-cockpit).
install-cockpit:
	install -d $(DESTDIR)$(COCKPITDIR)
	install -m 0644 cockpit/custos/*.html cockpit/custos/*.js cockpit/custos/*.css cockpit/custos/*.svg \
		cockpit/custos/manifest.json $(DESTDIR)$(COCKPITDIR)/

# The user guide and the contract, for people and for mcp-docs
# (package custos-docs).
install-doc:
	install -d $(DESTDIR)$(DOCDIR)/user-guide
	install -m 0644 docs/user-guide/*.md $(DESTDIR)$(DOCDIR)/user-guide/
	install -m 0644 CONTRACT.md $(DESTDIR)$(DOCDIR)/CONTRACT.md
	install -D -m 0644 packaging/mcp-docs/custos.yaml $(DESTDIR)$(MCPDOCSDIR)/custos.yaml

install-selinux: selinux
	install -d $(DESTDIR)$(SELINUXDIR)
	bzip2 -9 -c selinux/custos.pp >$(DESTDIR)$(SELINUXDIR)/custos.pp.bz2
	chmod 0644 $(DESTDIR)$(SELINUXDIR)/custos.pp.bz2

clean:
	rm -rf bin selinux/*.pp selinux/tmp
