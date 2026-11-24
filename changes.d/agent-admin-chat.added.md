- `agent-admin chat NAME` talks with an agent on the host's terminal,
  as Cockpit's chat does: each line is a message of one thread, and the
  answer is printed as it comes, with the agent's tool calls and the
  approvals it waits for. `--thread` continues a thread, `--message`
  sends one message and ends.
