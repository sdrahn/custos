- Cockpit stays quick while an agent does not answer, for example one
  whose service fails to start again and again while systemd holds its
  socket. Such an agent is shown as not answering, with the time it is
  asked again, after 30 seconds and then at growing intervals up to
  5 minutes; Start and Restart ask it again at once. Before, every
  refresh of the Agents and Activity views waited 8 seconds for it.
