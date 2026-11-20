- A chat thread is free again as soon as a client sees its run's end.
  Since 0.9.7, an answer to a client tool, or the next message, sent at
  once could be refused with `409` ("a run is in progress on this
  thread"), because the thread was released only after the stream had
  ended.
