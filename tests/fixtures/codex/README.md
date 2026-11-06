# Codex Responses fixtures

These fixtures document the offline Codex/OpenAI SDK compatibility profile for
Trajecta Responses server-mode.

They are wired into a focused offline fixture runner under
`internal/responses/httpapi` and runtime alignment tests under
`internal/responses/runtime`. The runner enumerates every current `.json` and
`.ndjson` fixture in this directory, validates request/response/error/stream
shape, and fails when the inventory changes without an explicit test update.
Use `task test:codex-fixtures` to run the gate. It does not depend on real
Codex, real model providers, wall clock time, or network access.

Coverage:

- `text_create_request.json` and `text_create_expected_response.json`: basic
  non-streaming Responses create.
- `stream_text_request.json` and `stream_text_events.ndjson`: minimal typed SSE
  text event sequence.
- `function_call_request.json` and `function_call_expected_response.json`:
  client-owned function tool call emission.
- `function_call_output_continuation_request.json`: client tool result
  continuation using `previous_response_id`.
- `ordinary_web_search_descriptor_request.json`: ordinary hosted web search
  descriptor accepted as compatibility input.
- `codex_web_search_absent_tools_request.json`: raw Codex web search-shaped
  request that omits `tools` and `tool_choice`; HTTP compatibility
  normalization may inject an available hosted search descriptor.
- `codex_chinese_news_stream_absent_tools_request.json`: raw Codex request for
  Chinese today's-news search with `stream:true` and no `tools` or
  `tool_choice`.
- `web_search_preview_extended_descriptor_request.json`: hosted
  `web_search_preview` descriptor carrying official extension fields such as
  `include`, `external_web_access`, and `search_content_types`.
- `forced_web_search_request.json`: forced hosted web search descriptor,
  documenting the execution-or-stable-rejection contract.
- `mcp_descriptor_request.json`: hosted `mcp` descriptor accepted as
  compatibility input (the executor itself is implemented in
  `internal/responses/tools/mcp`; a descriptor executes only when external
  servers are configured under `tools.mcp`).
- `unsupported_mcp_request.json`, `unsupported_file_search_request.json`,
  `unsupported_code_interpreter_request.json`, and
  `unsupported_computer_use_preview_request.json`: standalone request examples
  for hosted tools that must be rejected in this shape — the `mcp` fixture has
  no configured external server, and `file_search` / `code_interpreter` /
  `computer_use_preview` have no executor at all.
- `unsupported_hosted_tool_expected_error.json`: expected stable error shape for
  hosted tools without a configured runtime (`mcp` with no configured server,
  plus `file_search`, `code_interpreter`, and `computer_use_preview`).
  Runtime and HTTP tests assert the stable `unsupported_tool` rejection path
  for forced hosted tool execution.
