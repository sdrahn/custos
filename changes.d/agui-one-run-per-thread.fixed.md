- A chat thread has one run at a time: a message sent while the
  thread's previous run still works is refused (`409`), and Cockpit says
  so. Before, both ran and mixed the conversation's history, as after a
  dropped connection of an OpenShell agent; the model then answered as
  if it had not read the question.
- The history sent to an OpenAI-compatible model has each tool result
  right after its call: a call without a result gets a "no result"
  answer, a result without a call is left out. A history mixed before
  can be read again. A call without arguments sends `{}`, not `null`.
