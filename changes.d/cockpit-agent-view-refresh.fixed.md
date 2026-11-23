- An agent's view in Cockpit no longer moves or closes things under you
  every 5 seconds. The refresh redraws it only when what it shows has
  changed (or a minute on, for the relative times), and not while an
  editor, form or confirmation in it is open, focused or not. The task
  categories' readiness and review show their last result at once and
  are read again at most every 30 seconds. Before, the whole view was
  rebuilt at every refresh, and the readiness read anew each time
  shrank and grew the section while you scrolled or edited.
