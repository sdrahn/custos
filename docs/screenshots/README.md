# Screenshots of the Cockpit page

Screenshots of the Custos page (`custos-cockpit`) in a real Cockpit on
openSUSE Leap 16 with SELinux enforcing, logged in as a person (carol).
The real-Cockpit test takes them: CI job `cockpit-vm`, which runs
`test/vm/run-vm.sh` with `SUITE=cockpit`
([vm-setup.sh](../../test/cockpit/vm-setup.sh) sets up the agents and
activities, [vm-test.js](../../test/cockpit/vm-test.js) drives the page).
The agents run on custos-tests' scripted model, so the agents' texts
are the test's.

| File | Shows |
|---|---|
| `activity.png` | the Activity tab: triage's scheduled activity delegated to patch-pilot, whose change waits for an approval, with its chain |
| `approvals.png` | the approval item: the call from mcp-gateway beside the agent's self-asserted account; a decision of today |
| `agents.png` | the Agents tab: the agents, each with its Chat button, and patch-pilot's state, autonomy mode and the contents of its view |
| `policy.png` | the Policy tab: every agent's posture and what the gateway would decide |
| `audit.png` | the Audit tab: proposed → approved by carol via cockpit → executed, and a decline |
| `chat.png` | chat with patch-pilot over AG-UI, in the popup over the list of agents |
| `stopped.png` | after *Stop all agents*: the banner with who stopped them |

To take them again: run the job and decode them from its log (the job
prints them base64 between `SHOT-BEGIN` and `SHOT-END`), or download its
artifact `cockpit-screenshots`:

```
gh api repos/sdrahn/custos/actions/jobs/JOB/logs > job.log
python3 test/cockpit/screenshots-from-log.py job.log docs/screenshots
```
