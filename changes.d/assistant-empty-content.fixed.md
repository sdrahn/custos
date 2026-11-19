- An agent's requests to an OpenAI-compatible endpoint (`provider:
  openai`) give a model's message with tool calls only the content `""`,
  where they left the field out. The API allows both, but the chat
  templates of local servers read it from every message: with Gemma in
  LM Studio, a conversation with such a message failed with `Error
  rendering prompt with jinja template: "Cannot perform operation in on
  undefined values"`.
