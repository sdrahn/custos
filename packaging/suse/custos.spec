#
# spec file for package custos
#
# Built on the Open Build Service for SUSE Linux Enterprise Server 16.0
# and 16.1 and openSUSE Leap 16.0 and 16.1; see packaging/suse/README.md.
# Sources: the tarball and vendor.tar.gz (Go modules, OBS builds are
# offline) come from the _service file.
#

%define selinuxtype targeted
%define modulename custos

Name:           custos
Version:        0.6.0
Release:        0
Summary:        Governed agents on the host, supervised from Cockpit
# MIT for custos; the others for the vendored Go modules linked in.
License:        MIT AND Apache-2.0 AND BSD-2-Clause AND BSD-3-Clause
Group:          System/Management
URL:            https://github.com/sdrahn/custos
Source0:        %{name}-%{version}.tar.gz
Source1:        vendor.tar.gz
Source99:       %{name}-rpmlintrc
BuildRequires:  golang(API) >= 1.25
BuildRequires:  make
BuildRequires:  pkgconfig(systemd)
BuildRequires:  sysuser-tools
BuildRequires:  systemd-rpm-macros
# The agents' tools, policy and approvals are mcp-gateway's (CONTRACT.md §1).
# NVIDIA OpenShell is optional (sandbox kind openshell, CONTRACT.md §16a).
Suggests:       openshell
Requires:       mcp-gateway >= 1.16
Requires(pre):  group(mcp-users)
Requires:       (%{name}-selinux if selinux-policy-%{selinuxtype})
%sysusers_requires
%{?systemd_ordering}

%description
custos runs AI agents (Google ADK) on the host, each as its
own systemd service and system user, governed by mcp-gateway: the
agents' only tools are the gateway's, its OPA policy decides what each
agent may do in its autonomy mode (advisory, supervised, bounded), and
state-changing calls wait for a human approval. Agents are started by
people (chat, AG-UI), schedules or other agents (delegation through the
gateway), and every activity is recorded in the journal.

%package selinux
Summary:        SELinux policy module for custos
Group:          System/Management
BuildArch:      noarch
# mcp_gateway.if, the interfaces the module uses.
BuildRequires:  mcp-gateway-selinux
BuildRequires:  selinux-policy-devel
Requires:       mcp-gateway-selinux
Requires:       selinux-policy-%{selinuxtype}
Requires(post): selinux-policy-%{selinuxtype}
%{?_selinux_policy_version:%global _selinux_policy_version %(echo '%{_selinux_policy_version}' | cut -d- -f1)}
%{?selinux_requires}

%description selinux
SELinux domain agent_t for custos's agents (they may reach the
gateway's MCP socket, never its control socket nor another agent's
socket), and mcpsrv_agents_t for the gateway's delegation server.

%package cockpit
Summary:        Cockpit page for custos's agents
Group:          System/Management
BuildArch:      noarch
Requires:       %{name} = %{version}
Requires:       cockpit-bridge
Recommends:     mcp-gateway-cockpit

%description cockpit
The Custos page in Cockpit: what the agents on the host do (their
activities, who started them, delegation chains, tool calls) and the
calls waiting for an approval, decided through mcp-gateway as the
logged-in person. Members of agent-clients see the agents; mcp-gateway's
approver rules say who may decide.

%package tests
Summary:        Functional tests of custos, for openQA
Group:          Development/Tools/Other
Requires:       %{name} = %{version}
Requires:       mcp-gateway >= 1.16
# What the tests run: the gateway's control API and the agents' sockets
# (curl, jq), the journal, nsenter and runuser (util-linux), and the
# kernel audit log and SELinux tools for the denial check.
Requires:       audit
Requires:       curl
Requires:       jq
Requires:       policycoreutils
Requires:       shadow
Requires:       util-linux
Recommends:     %{name}-selinux = %{version}

%description tests
Functional tests of custos on an installed system: agents
created with agent-admin and run by systemd, governed by mcp-gateway
(supervised by default, approvals, autonomy modes, pause, emergency
stop), schedules, AG-UI, delegation, the journal records, the sandbox
and SELinux. With a scripted model server and an MCP test server, so
no model is needed. For openQA and other test systems:
/usr/libexec/custos/tests/run-tests.sh --disposable --results DIR.
model-eval.sh measures how well a model does Custos's work: the bundled
diagnostics install's playbooks against planted faults, and the
prompt-injection cases (--disposable --model URL NAME).
They change the system for good; install them on test systems only.

%package diagnostics
Summary:        The bundled health-check and diagnostics agent of custos
Group:          System/Management
Requires:       %{name} = %{version}
Requires:       mcp-gateway-exec-server
# update-status's services to restart (pending_restarts): a server with
# CAP_SYS_PTRACE, so installing it is the administrator's choice;
# custos-setup links its profile when it is installed.
Suggests:       mcp-gateway-restarts-server
Recommends:     %{name}-cockpit = %{version}
Recommends:     %{name}-selinux = %{version}

%description diagnostics
The bundled health-check and diagnostics install, which the installer
offers as an opt-in choice: custos-setup, which the installer runs to
make the host diagnostics-only (only the diagnostics task category is
usable until an administrator enables more with agent-admin enable),
set the model, create the agent health-check from its template, and
install the diagnostic playbooks' commands for mcp-gateway's exec
server. The playbooks and the template are in custos.

%package docs
Summary:        User guide of custos, also for mcp-docs
Group:          Documentation/Other
BuildArch:      noarch
# Installed with mcp-docs, it serves the guide to agents.
Supplements:    (%{name} and mcp-docs)

%description docs
The Custos user guide: installing, connecting to mcp-gateway and an
identity provider, configuring, administering and supervising agents,
the audit record, OpenShell sandboxes, troubleshooting and reference,
and the contract between Custos's parts, in /usr/share/custos/docs.
With a collection file for mcp-docs, which then serves them as the
collection custos to the agents and assistants on the host.

%prep
%autosetup -p1 -a1

%build
export GOFLAGS="-mod=vendor"
%make_build build build-tests VERSION=%{version}
make selinux
%sysusers_generate_pre packaging/sysusers/custos.conf %{name} %{name}.conf

%install
%make_install install install-selinux install-tests install-cockpit install-doc \
    PREFIX=%{_prefix} LIBEXECDIR=%{_libexecdir} DATADIR=%{_datadir} SBINDIR=%{_sbindir} \
    SYSCONFDIR=%{_sysconfdir} UNITDIR=%{_unitdir} SYSUSERSDIR=%{_sysusersdir} \
    TMPFILESDIR=%{_tmpfilesdir} SELINUXDIR=%{_datadir}/selinux/packages/%{selinuxtype} \
    COCKPITDIR=%{_datadir}/cockpit/custos DOCDIR=%{_datadir}/custos/docs \
    MCPDOCSDIR=%{_datadir}/mcp-docs/collections.d

%check
export GOFLAGS="-mod=vendor"
go test ./internal/... ./test/...

%pre -f %{name}.pre
%service_add_pre custos-demote.service custos-demote.timer

%post
%tmpfiles_create %{_tmpfilesdir}/%{name}.conf
# The demotion evaluator's timer stays as the presets say (off);
# agent-admin promote enables it with the first bounded category.
%service_add_post custos-demote.service custos-demote.timer
# mcp-gateway's OPA watches only the directories it loaded a file from
# when it started: once restarted, it watches the agents' directory
# (with _control.yaml) and loads agent-admin's changes there by itself.
if [ $1 -eq 1 ]; then
    systemctl try-restart mcp-opa.service >/dev/null 2>&1 || :
fi
# Agents are template instances, enabled by agent-admin create.

%preun
%service_del_preun custos-demote.service custos-demote.timer

%postun
%service_del_postun custos-demote.service custos-demote.timer
# Running agents keep the old launcher until they exit on idle or are
# restarted; restarting them here could cut off an activity waiting for
# an approval.

%pre selinux
%selinux_relabel_pre -s %{selinuxtype}

%post selinux
%selinux_modules_install -s %{selinuxtype} %{_datadir}/selinux/packages/%{selinuxtype}/%{modulename}.pp.bz2

%postun selinux
if [ $1 -eq 0 ]; then
    %selinux_modules_uninstall -s %{selinuxtype} %{modulename}
fi

%posttrans selinux
%selinux_relabel_post -s %{selinuxtype}
# What was installed or made before the module was loaded (the programs,
# the main package's tmpfiles, agents' state) gets the module's labels:
# a launcher left bin_t would run outside agent_t.
for d in %{_libexecdir}/custos /run/agents /var/lib/agents /etc/custos; do
    [ -e "$d" ] && restorecon -R "$d" || :
done

%files
%license LICENSE
%doc README.md CONTRACT.md
%{_sbindir}/agent-admin
%dir %{_libexecdir}/custos
%{_libexecdir}/custos/agent-launcher
%{_libexecdir}/custos/agent-delegate
%{_libexecdir}/custos/agent-openshell-relay
%{_libexecdir}/custos/agent-openshell-logs
%{_libexecdir}/custos/agent-openshell-start
%{_libexecdir}/custos/agent-admin
%{_unitdir}/agent@.service
%{_unitdir}/agent@.socket
%{_unitdir}/agent-schedule@.service
%{_unitdir}/agent-schedule@.timer
%{_unitdir}/agent-event@.service
%{_unitdir}/agent-openshell@.service
%{_unitdir}/agent-openshell@.socket
%{_unitdir}/custos-demote.service
%{_unitdir}/custos-demote.timer
%{_sysusersdir}/%{name}.conf
%{_tmpfilesdir}/%{name}.conf
%dir %{_sysconfdir}/custos
%dir %{_sysconfdir}/custos/agents
%dir %{_sysconfdir}/custos/categories.d
%dir %{_sysconfdir}/custos/templates
%dir %{_sysconfdir}/custos/playbooks
%dir %{_datadir}/mcp-gateway
%dir %{_datadir}/mcp-gateway/policy
%{_datadir}/mcp-gateway/policy/custos
%dir %{_sysconfdir}/mcp-gateway
%dir %{_sysconfdir}/mcp-gateway/policy
%dir %{_sysconfdir}/mcp-gateway/policy/agents
%config(noreplace) %{_sysconfdir}/mcp-gateway/policy/agents/_control.yaml
%config(noreplace) %{_sysconfdir}/mcp-gateway/policy/agents/_http.yaml
%dir %{_datadir}/custos
%{_datadir}/custos/categories.d
%{_datadir}/custos/examples
%{_datadir}/custos/gateway
%{_datadir}/custos/openshell
%{_datadir}/custos/playbooks
%{_datadir}/custos/templates

%files diagnostics
%{_sbindir}/custos-setup

%files docs
%dir %{_datadir}/custos
%{_datadir}/custos/docs
%dir %{_datadir}/mcp-docs
%dir %{_datadir}/mcp-docs/collections.d
%{_datadir}/mcp-docs/collections.d/custos.yaml

%files cockpit
%dir %{_datadir}/cockpit
%{_datadir}/cockpit/custos

%files tests
%dir %{_libexecdir}/custos
%dir %{_libexecdir}/custos/tests
%{_libexecdir}/custos/tests/run-tests.sh
%{_libexecdir}/custos/tests/model-eval.sh
%{_libexecdir}/custos/tests/fakemodel
%{_libexecdir}/custos/tests/testsrv
%{_libexecdir}/custos/tests/testidp
%{_libexecdir}/custos/tests/secondhost

%files selinux
%dir %{_datadir}/selinux/packages/%{selinuxtype}
%{_datadir}/selinux/packages/%{selinuxtype}/%{modulename}.pp.bz2

%changelog
