package convert_test

// Known cross-engine divergences (P5 parity audit). Each entry is one
// (direction, from, to, case) cell of the parity matrix whose legacy and xir
// production behavior differs. The parity tests skip these cells with the
// recorded reason; TestParityKnownDivergences re-runs them and FAILS when a
// divergence no longer reproduces, so an engine fix is a deliberate promotion
// signal: move the cell out of this registry (it then becomes a resident
// parity assertion) and update .scratch/audit/p5-parity.md.
//
// Kind values: "decision" (accept/reject differs), "semantic" (both accept,
// converted meaning differs), "category" (both reject, rejection category
// differs; proxy-visible status is the same).
//
// This registry is populated from the TRAJECTA_PARITY_DUMP matrix; see the
// audit report for minimal reproductions and impact assessment.
//
// P5 legacy-fix milestone (.scratch/audit/p5-legacy-fix.md): the legacy
// engine's S-INPUT-TEXT / S-GEM-PARALLEL / S-GEM-ARGS defects were fixed, so
// the 34 cells whose only root cause was S-INPUT-TEXT were promoted out of
// this registry into resident parity assertions. Cells with compound causes
// stay pinned under their REMAINING root causes only (envelope synthesis,
// xir-side tool ids, usage detail, policy items); the fixed causes no longer
// appear in any reason.

import (
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/protocolconvert"
)

type parityDivergence struct {
	Kind   string // decision | semantic | category
	Reason string
}

var knownParityDivergences = map[string]parityDivergence{
	"request|chat_completions|gemini|chat_functions_legacy":               {Kind: "decision", Reason: "D-SILENT-DROP+D-MAP-IMPROVE: legacy fatal refusal; xir silently drops the answer-changing field (no loss entry)"},
	"request|chat_completions|gemini|chat_image_data_uri":                 {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|chat_completions|gemini|chat_image_url":                      {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|chat_completions|gemini|chat_input_audio":                    {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|chat_completions|gemini|chat_logit_bias":                     {Kind: "decision", Reason: "D-SILENT-DROP: legacy fatal refusal; xir silently drops the answer-changing field (no loss entry)"},
	"request|chat_completions|gemini|chat_n2":                             {Kind: "decision", Reason: "D-SILENT-DROP: legacy fatal refusal; xir silently drops the answer-changing field (no loss entry)"},
	"request|chat_completions|gemini|chat_parallel_tool_calls_false":      {Kind: "decision", Reason: "D-PARALLEL: legacy fatal on parallel_tool_calls:false; xir non-fatal drop"},
	"request|chat_completions|gemini|chat_presence_penalty":               {Kind: "decision", Reason: "D-MAP-IMPROVE: legacy fatal refusal; xir maps the field to a target-protocol equivalent"},
	"request|chat_completions|gemini|chat_reasoning_effort":               {Kind: "decision", Reason: "D-MAP-IMPROVE: legacy fatal refusal; xir maps the field to a target-protocol equivalent"},
	"request|chat_completions|gemini|chat_reasoning_history":              {Kind: "decision", Reason: "D-REASON-HIST: legacy rejects chat reasoning_content history; xir rewrites it to an unsigned thinking block"},
	"request|chat_completions|gemini|chat_seed":                           {Kind: "decision", Reason: "D-MAP-IMPROVE: legacy fatal refusal; xir maps the field to a target-protocol equivalent"},
	"request|chat_completions|gemini|chat_stream_number":                  {Kind: "decision", Reason: "D-STREAM-SPELL: legacy rejects stream:\"true\"/1 spellings; xir silently drops the flag and converts"},
	"request|chat_completions|gemini|chat_stream_string":                  {Kind: "decision", Reason: "D-STREAM-SPELL: legacy rejects stream:\"true\"/1 spellings; xir silently drops the flag and converts"},
	"request|chat_completions|gemini|chat_tool_choice_unknown":            {Kind: "decision", Reason: "D-SILENT-DROP: legacy fatal refusal; xir silently drops the answer-changing field (no loss entry)"},
	"request|chat_completions|gemini|chat_tool_strict":                    {Kind: "decision", Reason: "D-STRICT: legacy fatal on strict tool schema; xir carries strict to Messages / non-fatal to Gemini"},
	"request|chat_completions|gemini|chat_top_k":                          {Kind: "semantic", Reason: "S-TOPK-STOP: top_k/stop handling differs (xir emits top_k to OpenAI surfaces; stop string vs array; legacy drops topK to Gemini)"},
	"request|chat_completions|messages|chat_developer_role":               {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|chat_completions|messages|chat_empty_object":                 {Kind: "semantic", Reason: "S-MAXTOK: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|chat_completions|messages|chat_functions_legacy":             {Kind: "decision", Reason: "D-SILENT-DROP+D-MAP-IMPROVE: legacy fatal refusal; xir silently drops the answer-changing field (no loss entry)"},
	"request|chat_completions|messages|chat_image_data_uri":               {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|chat_completions|messages|chat_image_url":                    {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|chat_completions|messages|chat_input_audio":                  {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|chat_completions|messages|chat_logit_bias":                   {Kind: "decision", Reason: "D-SILENT-DROP: legacy fatal refusal; xir silently drops the answer-changing field (no loss entry)"},
	"request|chat_completions|messages|chat_max_completion_tokens":        {Kind: "semantic", Reason: "S-CONTENT-REPR: content string vs text-block array representation (both protocol-valid)"},
	"request|chat_completions|messages|chat_max_tokens":                   {Kind: "semantic", Reason: "S-CONTENT-REPR: content string vs text-block array representation (both protocol-valid)"},
	"request|chat_completions|messages|chat_multiturn":                    {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|chat_completions|messages|chat_n2":                           {Kind: "decision", Reason: "D-SILENT-DROP: legacy fatal refusal; xir silently drops the answer-changing field (no loss entry)"},
	"request|chat_completions|messages|chat_parallel_tool_calls_false":    {Kind: "decision", Reason: "D-PARALLEL: legacy fatal on parallel_tool_calls:false; xir non-fatal drop"},
	"request|chat_completions|messages|chat_reasoning_history":            {Kind: "decision", Reason: "D-REASON-HIST: legacy rejects chat reasoning_content history; xir rewrites it to an unsigned thinking block"},
	"request|chat_completions|messages|chat_sampling":                     {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|chat_completions|messages|chat_stream_number":                {Kind: "decision", Reason: "D-STREAM-SPELL: legacy rejects stream:\"true\"/1 spellings; xir silently drops the flag and converts"},
	"request|chat_completions|messages|chat_stream_string":                {Kind: "decision", Reason: "D-STREAM-SPELL: legacy rejects stream:\"true\"/1 spellings; xir silently drops the flag and converts"},
	"request|chat_completions|messages|chat_system_blocks":                {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|chat_completions|messages|chat_text_plain":                   {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|chat_completions|messages|chat_tool_choice_auto":             {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|chat_completions|messages|chat_tool_choice_empty":            {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|chat_completions|messages|chat_tool_choice_named":            {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|chat_completions|messages|chat_tool_choice_none":             {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|chat_completions|messages|chat_tool_choice_required":         {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|chat_completions|messages|chat_tool_choice_unknown":          {Kind: "decision", Reason: "D-SILENT-DROP: legacy fatal refusal; xir silently drops the answer-changing field (no loss entry)"},
	"request|chat_completions|messages|chat_tool_loop":                    {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|chat_completions|messages|chat_tool_loop_parallel":           {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|chat_completions|messages|chat_tool_strict":                  {Kind: "decision", Reason: "D-STRICT: legacy fatal on strict tool schema; xir carries strict to Messages / non-fatal to Gemini"},
	"request|chat_completions|messages|chat_tools":                        {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|chat_completions|messages|chat_top_k":                        {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR+S-TOPK-STOP: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|chat_completions|messages|chat_unknown_field":                {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|chat_completions|messages|chat_user_metadata":                {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR+S-METADATA: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|chat_completions|messages|chat_vertex_text":                  {Kind: "semantic", Reason: "S-CONTENT-REPR: content string vs text-block array representation (both protocol-valid)"},
	"request|chat_completions|responses|chat_developer_role":              {Kind: "semantic", Reason: "S-SYS-PLACE: system prompt placement differs (instructions vs system input item; both valid)"},
	"request|chat_completions|responses|chat_functions_legacy":            {Kind: "decision", Reason: "D-SILENT-DROP+D-MAP-IMPROVE: legacy fatal refusal; xir silently drops the answer-changing field (no loss entry)"},
	"request|chat_completions|responses|chat_image_data_uri":              {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|chat_completions|responses|chat_image_url":                   {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|chat_completions|responses|chat_input_audio":                 {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|chat_completions|responses|chat_logit_bias":                  {Kind: "decision", Reason: "D-SILENT-DROP: legacy fatal refusal; xir silently drops the answer-changing field (no loss entry)"},
	"request|chat_completions|responses|chat_multiturn":                   {Kind: "semantic", Reason: "S-SYS-PLACE: system prompt placement differs (instructions vs system input item; both valid)"},
	"request|chat_completions|responses|chat_n2":                          {Kind: "decision", Reason: "D-SILENT-DROP: legacy fatal refusal; xir silently drops the answer-changing field (no loss entry)"},
	"request|chat_completions|responses|chat_parallel_tool_calls_false":   {Kind: "decision", Reason: "D-PARALLEL: legacy fatal on parallel_tool_calls:false; xir non-fatal drop"},
	"request|chat_completions|responses|chat_reasoning_effort":            {Kind: "decision", Reason: "D-MAP-IMPROVE: legacy fatal refusal; xir maps the field to a target-protocol equivalent"},
	"request|chat_completions|responses|chat_reasoning_history":           {Kind: "decision", Reason: "D-REASON-HIST: legacy rejects chat reasoning_content history; xir rewrites it to an unsigned thinking block"},
	"request|chat_completions|responses|chat_response_format":             {Kind: "decision", Reason: "D-MAP-IMPROVE: legacy fatal refusal; xir maps the field to a target-protocol equivalent"},
	"request|chat_completions|responses|chat_stream_number":               {Kind: "decision", Reason: "D-STREAM-SPELL: legacy rejects stream:\"true\"/1 spellings; xir silently drops the flag and converts"},
	"request|chat_completions|responses|chat_stream_string":               {Kind: "decision", Reason: "D-STREAM-SPELL: legacy rejects stream:\"true\"/1 spellings; xir silently drops the flag and converts"},
	"request|chat_completions|responses|chat_system_blocks":               {Kind: "semantic", Reason: "S-SYS-PLACE: system prompt placement differs (instructions vs system input item; both valid)"},
	"request|chat_completions|responses|chat_tool_choice_unknown":         {Kind: "decision", Reason: "D-SILENT-DROP: legacy fatal refusal; xir silently drops the answer-changing field (no loss entry)"},
	"request|chat_completions|responses|chat_tool_loop":                   {Kind: "semantic", Reason: "S-NAME: optional name member on function_call_output (legacy carries it, xir omits; both valid)"},
	"request|chat_completions|responses|chat_tool_loop_parallel":          {Kind: "semantic", Reason: "S-NAME: optional name member on function_call_output (legacy carries it, xir omits; both valid)"},
	"request|chat_completions|responses|chat_top_k":                       {Kind: "semantic", Reason: "S-TOPK-STOP: xir emits top_k to the Responses surface; legacy drops it with a warning"},
	"request|chat_completions|responses|chat_user_metadata":               {Kind: "semantic", Reason: "S-METADATA: metadata/user mapping differs (legacy coerces to user_id; xir preserves metadata)"},
	"request|gemini|chat_completions|gem_file_data":                       {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|gemini|chat_completions|gem_inline_data":                     {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|gemini|chat_completions|gem_response_schema":                 {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|gemini|chat_completions|gem_sampling":                        {Kind: "semantic", Reason: "S-TOPK-STOP: top_k/stop handling differs (xir emits top_k to OpenAI surfaces; stop string vs array; legacy drops topK to Gemini)"},
	"request|gemini|chat_completions|gem_stream_flag":                     {Kind: "category", Reason: "C-GEMSTREAM: both reject non-standard gemini stream flag; legacy streaming vs xir extensions fatal"},
	"request|gemini|chat_completions|gem_thinking_config":                 {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|gemini|chat_completions|gem_thought_signature":               {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|gemini|chat_completions|gem_tool_loop":                       {Kind: "decision", Reason: "D-GEM-TOOLS: legacy rejects Gemini functionCall/functionResponse history; xir carries the tool loop"},
	"request|gemini|chat_completions|gem_tool_loop_parallel":              {Kind: "decision", Reason: "D-GEM-TOOLS: legacy rejects Gemini functionCall/functionResponse history; xir carries the tool loop"},
	"request|gemini|chat_completions|gem_unknown_field":                   {Kind: "decision", Reason: "D-EXT-REQ: xir Strict fatals on unknown request/response fields via extensions.<proto> (legacy: non-fatal warning)"},
	"request|gemini|messages|gem_empty_object":                            {Kind: "semantic", Reason: "S-MAXTOK: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|gemini|messages|gem_file_data":                               {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|gemini|messages|gem_inline_data":                             {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|gemini|messages|gem_max_output_tokens":                       {Kind: "semantic", Reason: "S-CONTENT-REPR: content string vs text-block array representation (both protocol-valid)"},
	"request|gemini|messages|gem_multiturn":                               {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|gemini|messages|gem_response_schema":                         {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|gemini|messages|gem_sampling":                                {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|gemini|messages|gem_stream_flag":                             {Kind: "category", Reason: "C-GEMSTREAM: both reject non-standard gemini stream flag; legacy streaming vs xir extensions fatal"},
	"request|gemini|messages|gem_system_instruction":                      {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|gemini|messages|gem_text_plain":                              {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|gemini|messages|gem_thinking_config":                         {Kind: "decision", Reason: "D-MAP-IMPROVE: legacy fatal refusal; xir maps the field to a target-protocol equivalent"},
	"request|gemini|messages|gem_thought_signature":                       {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|gemini|messages|gem_tool_choice_any":                         {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|gemini|messages|gem_tool_choice_auto":                        {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|gemini|messages|gem_tool_choice_named":                       {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|gemini|messages|gem_tool_choice_none":                        {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|gemini|messages|gem_tool_loop":                               {Kind: "decision", Reason: "D-GEM-TOOLS: legacy rejects Gemini functionCall/functionResponse history; xir carries the tool loop"},
	"request|gemini|messages|gem_tool_loop_parallel":                      {Kind: "decision", Reason: "D-GEM-TOOLS: legacy rejects Gemini functionCall/functionResponse history; xir carries the tool loop"},
	"request|gemini|messages|gem_tools":                                   {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|gemini|messages|gem_unknown_field":                           {Kind: "decision", Reason: "D-EXT-REQ: xir Strict fatals on unknown request/response fields via extensions.<proto> (legacy: non-fatal warning)"},
	"request|gemini|messages|gem_vertex_text":                             {Kind: "semantic", Reason: "S-CONTENT-REPR: content string vs text-block array representation (both protocol-valid)"},
	"request|gemini|responses|gem_file_data":                              {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|gemini|responses|gem_inline_data":                            {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|gemini|responses|gem_response_schema":                        {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|gemini|responses|gem_sampling":                               {Kind: "semantic", Reason: "S-TOPK-STOP: xir emits top_k to the Responses surface; legacy drops it with a warning"},
	"request|gemini|responses|gem_stream_flag":                            {Kind: "category", Reason: "C-GEMSTREAM: both reject non-standard gemini stream flag; legacy streaming vs xir extensions fatal"},
	"request|gemini|responses|gem_thinking_config":                        {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|gemini|responses|gem_thought_signature":                      {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|gemini|responses|gem_tool_loop":                              {Kind: "decision", Reason: "D-GEM-TOOLS: legacy rejects Gemini functionCall/functionResponse history; xir carries the tool loop"},
	"request|gemini|responses|gem_tool_loop_parallel":                     {Kind: "decision", Reason: "D-GEM-TOOLS: legacy rejects Gemini functionCall/functionResponse history; xir carries the tool loop"},
	"request|gemini|responses|gem_unknown_field":                          {Kind: "decision", Reason: "D-EXT-REQ: xir Strict fatals on unknown request/response fields via extensions.<proto> (legacy: non-fatal warning)"},
	"request|messages|chat_completions|msg_image":                         {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|messages|chat_completions|msg_metadata":                      {Kind: "semantic", Reason: "S-METADATA: metadata/user mapping differs (legacy coerces to user_id; xir preserves metadata)"},
	"request|messages|chat_completions|msg_redacted_thinking":             {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|messages|chat_completions|msg_sampling":                      {Kind: "semantic", Reason: "S-TOPK-STOP: top_k/stop handling differs (xir emits top_k to OpenAI surfaces; stop string vs array; legacy drops topK to Gemini)"},
	"request|messages|chat_completions|msg_thinking":                      {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|messages|chat_completions|msg_tool_result_blocks":            {Kind: "semantic", Reason: "S-CONTENT-REPR: content string vs text-block array representation (both protocol-valid)"},
	"request|messages|chat_completions|msg_unknown_field":                 {Kind: "decision", Reason: "D-EXT-REQ: xir Strict fatals on unknown request/response fields via extensions.<proto> (legacy: non-fatal warning)"},
	"request|messages|gemini|msg_image":                                   {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|messages|gemini|msg_redacted_thinking":                       {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|messages|gemini|msg_thinking":                                {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|messages|gemini|msg_thinking_param":                          {Kind: "decision", Reason: "D-MAP-IMPROVE: legacy fatal refusal; xir maps the field to a target-protocol equivalent"},
	"request|messages|gemini|msg_unknown_field":                           {Kind: "decision", Reason: "D-EXT-REQ: xir Strict fatals on unknown request/response fields via extensions.<proto> (legacy: non-fatal warning)"},
	"request|messages|responses|msg_image":                                {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|messages|responses|msg_metadata":                             {Kind: "semantic", Reason: "S-METADATA: metadata/user mapping differs (legacy coerces to user_id; xir preserves metadata)"},
	"request|messages|responses|msg_redacted_thinking":                    {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|messages|responses|msg_sampling":                             {Kind: "semantic", Reason: "S-TOPK-STOP: xir emits top_k to the Responses surface; legacy drops it with a warning"},
	"request|messages|responses|msg_thinking":                             {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|messages|responses|msg_tool_result_blocks":                   {Kind: "semantic", Reason: "S-STRUCT-RESULT: structured tool-result rendering differs (text wrap vs object/array output)"},
	"request|messages|responses|msg_unknown_field":                        {Kind: "decision", Reason: "D-EXT-REQ: xir Strict fatals on unknown request/response fields via extensions.<proto> (legacy: non-fatal warning)"},
	"request|responses|chat_completions|resp_hosted_tool":                 {Kind: "decision", Reason: "D-HOSTED: legacy rejects hosted tool declarations; xir carries them by type/kind"},
	"request|responses|chat_completions|resp_input_image":                 {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|responses|chat_completions|resp_previous_response_id":        {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|responses|chat_completions|resp_reasoning_effort":            {Kind: "decision", Reason: "D-MAP-IMPROVE: legacy fatal refusal; xir maps the field to a target-protocol equivalent"},
	"request|responses|chat_completions|resp_reasoning_encrypted":         {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|responses|chat_completions|resp_store_cache_key":             {Kind: "semantic", Reason: "S-STORE: xir carries store/prompt_cache_key to Chat; legacy drops with warning"},
	"request|responses|chat_completions|resp_text_format":                 {Kind: "decision", Reason: "D-MAP-IMPROVE: legacy fatal refusal; xir maps the field to a target-protocol equivalent"},
	"request|responses|chat_completions|resp_text_format_default":         {Kind: "semantic", Reason: "S-DEFAULT-FMT-OUT: xir emits the default response_format explicitly; legacy omits (equivalent)"},
	"request|responses|chat_completions|resp_unknown_field":               {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"request|responses|gemini|resp_hosted_tool":                           {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|responses|gemini|resp_input_image":                           {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|responses|gemini|resp_previous_response_id":                  {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|responses|gemini|resp_reasoning_effort":                      {Kind: "decision", Reason: "D-MAP-IMPROVE: legacy fatal refusal; xir maps the field to a target-protocol equivalent"},
	"request|responses|gemini|resp_reasoning_encrypted":                   {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|responses|gemini|resp_text_format_default":                   {Kind: "decision", Reason: "D-DEFAULT-FMT: xir fatals on the DEFAULT Responses text.format; legacy treats it as no loss"},
	"request|responses|gemini|resp_tool_result_structured":                {Kind: "semantic", Reason: "S-STRUCT-RESULT: structured tool-result rendering differs (text wrap vs object/array output)"},
	"request|responses|gemini|resp_unknown_field":                         {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"request|responses|messages|resp_empty_object":                        {Kind: "semantic", Reason: "S-MAXTOK: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|responses|messages|resp_hosted_tool":                         {Kind: "decision", Reason: "D-HOSTED: legacy rejects hosted tool declarations; xir carries them by type/kind"},
	"request|responses|messages|resp_input_image":                         {Kind: "decision", Reason: "D-MEDIA: legacy structurally rejects multimodal; xir carries media parts"},
	"request|responses|messages|resp_instructions":                        {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|responses|messages|resp_max_output_tokens":                   {Kind: "semantic", Reason: "S-CONTENT-REPR: content string vs text-block array representation (both protocol-valid)"},
	"request|responses|messages|resp_multiturn_items":                     {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|responses|messages|resp_previous_response_id":                {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|responses|messages|resp_reasoning_encrypted":                 {Kind: "category", Reason: "C-STRUCT: both reject; legacy structural pre-check (invalid) vs xir fatal loss ledger (same proxy 4xx)"},
	"request|responses|messages|resp_sampling":                            {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|responses|messages|resp_store_cache_key":                     {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|responses|messages|resp_text_format_default":                 {Kind: "decision", Reason: "D-DEFAULT-FMT: xir fatals on the DEFAULT Responses text.format; legacy treats it as no loss"},
	"request|responses|messages|resp_text_plain":                          {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|responses|messages|resp_tool_choice_auto":                    {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|responses|messages|resp_tool_choice_named":                   {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|responses|messages|resp_tool_choice_none":                    {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|responses|messages|resp_tool_choice_required":                {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|responses|messages|resp_tool_loop":                           {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|responses|messages|resp_tool_loop_parallel":                  {Kind: "semantic", Reason: "S-MAXTOK: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|responses|messages|resp_tool_result_structured":              {Kind: "semantic", Reason: "S-MAXTOK: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|responses|messages|resp_tools":                               {Kind: "semantic", Reason: "S-MAXTOK+S-CONTENT-REPR: xir omits the Messages-required max_tokens default (legacy injects 4096)"},
	"request|responses|messages|resp_unknown_field":                       {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|chat_completions|gemini|chat_resp_empty_choices":            {Kind: "decision", Reason: "D-EMPTY-CHOICES: xir decode rejects empty choices; legacy emits an empty shell"},
	"response|chat_completions|gemini|chat_resp_error":                    {Kind: "semantic", Reason: "S-ERROR-SHELL: legacy converts error envelopes to empty shells; xir preserves the error member"},
	"response|chat_completions|gemini|chat_resp_finish_content_filter":    {Kind: "semantic", Reason: "S-EMPTY-TEXT+S-ENVELOPE: empty text block vs empty content array (equivalent)"},
	"response|chat_completions|gemini|chat_resp_finish_length":            {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|chat_completions|gemini|chat_resp_logprobs":                 {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|chat_completions|gemini|chat_resp_reasoning":                {Kind: "semantic", Reason: "S-EMPTY-TEXT+S-ENVELOPE: empty text block vs empty content array (equivalent)"},
	"response|chat_completions|gemini|chat_resp_refusal":                  {Kind: "semantic", Reason: "S-EMPTY-TEXT+S-ENVELOPE: empty text block vs empty content array (equivalent)"},
	"response|chat_completions|gemini|chat_resp_sse_raw":                  {Kind: "category", Reason: "C-SSE: both reject SSE; legacy parse error (invalid) vs xir ErrStreamingConversion (same proxy 502)"},
	"response|chat_completions|gemini|chat_resp_text":                     {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|chat_completions|gemini|chat_resp_tool_call":                {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|chat_completions|gemini|chat_resp_tool_calls_parallel":      {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|chat_completions|gemini|chat_resp_usage_details":            {Kind: "semantic", Reason: "S-ENVELOPE+S-USAGE-DETAIL: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|chat_completions|messages|chat_resp_empty_choices":          {Kind: "decision", Reason: "D-EMPTY-CHOICES: xir decode rejects empty choices; legacy emits an empty shell"},
	"response|chat_completions|messages|chat_resp_error":                  {Kind: "semantic", Reason: "S-ERROR-SHELL+S-ENVELOPE: legacy converts error envelopes to empty shells; xir preserves the error member"},
	"response|chat_completions|messages|chat_resp_finish_content_filter":  {Kind: "semantic", Reason: "S-EMPTY-TEXT: empty text block vs empty content array (equivalent)"},
	"response|chat_completions|messages|chat_resp_reasoning":              {Kind: "semantic", Reason: "S-REASON-CARRY: response reasoning content carried by xir, dropped by legacy"},
	"response|chat_completions|messages|chat_resp_sse_raw":                {Kind: "category", Reason: "C-SSE: both reject SSE; legacy parse error (invalid) vs xir ErrStreamingConversion (same proxy 502)"},
	"response|chat_completions|messages|chat_resp_text":                   {Kind: "semantic", Reason: "S-USAGE-ZERO: legacy emits zero-valued cache usage fields; xir omits them (equivalent)"},
	"response|chat_completions|messages|chat_resp_tool_call":              {Kind: "semantic", Reason: "S-USAGE-ZERO: legacy emits zero-valued cache usage fields; xir omits them (equivalent)"},
	"response|chat_completions|messages|chat_resp_tool_calls_parallel":    {Kind: "semantic", Reason: "S-USAGE-ZERO: legacy emits zero-valued cache usage fields; xir omits them (equivalent)"},
	"response|chat_completions|messages|chat_resp_usage_details":          {Kind: "semantic", Reason: "S-USAGE-ZERO+S-USAGE-DETAIL: legacy emits zero-valued cache usage fields; xir omits them (equivalent)"},
	"response|chat_completions|responses|chat_resp_empty_choices":         {Kind: "decision", Reason: "D-EMPTY-CHOICES: xir decode rejects empty choices; legacy emits an empty shell"},
	"response|chat_completions|responses|chat_resp_error":                 {Kind: "semantic", Reason: "S-ENVELOPE+S-ERROR-SHELL: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|chat_completions|responses|chat_resp_finish_content_filter": {Kind: "semantic", Reason: "S-ENVELOPE+S-REFUSAL: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|chat_completions|responses|chat_resp_finish_length":         {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|chat_completions|responses|chat_resp_logprobs":              {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|chat_completions|responses|chat_resp_reasoning":             {Kind: "semantic", Reason: "S-ENVELOPE+S-REASON-CARRY: synthesized envelope members differ; response reasoning content carried by xir, dropped by legacy"},
	"response|chat_completions|responses|chat_resp_refusal":               {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|chat_completions|responses|chat_resp_sse_raw":               {Kind: "category", Reason: "C-SSE: both reject SSE; legacy parse error (invalid) vs xir ErrStreamingConversion (same proxy 502)"},
	"response|chat_completions|responses|chat_resp_text":                  {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|chat_completions|responses|chat_resp_tool_call":             {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|chat_completions|responses|chat_resp_tool_calls_parallel":   {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|chat_completions|responses|chat_resp_usage_details":         {Kind: "semantic", Reason: "S-ENVELOPE+S-USAGE-DETAIL: synthesized envelope members differ; cache/reasoning usage details carried by xir only"},
	"response|gemini|chat_completions|gem_resp_empty_candidates":          {Kind: "decision", Reason: "D-EXT-REQ: xir Strict fatals on unknown request/response fields via extensions.<proto> (legacy: non-fatal warning)"},
	"response|gemini|chat_completions|gem_resp_error":                     {Kind: "semantic", Reason: "S-ERROR-SHELL+S-ENVELOPE: legacy converts error envelopes to empty shells; xir preserves the error member"},
	"response|gemini|chat_completions|gem_resp_finish_max_tokens":         {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|gemini|chat_completions|gem_resp_finish_safety":             {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|gemini|chat_completions|gem_resp_function_call":             {Kind: "semantic", Reason: "S-TOOLID+S-ENVELOPE: tool-call id missing/empty in xir (Gemini source has no id); legacy synthesizes one"},
	"response|gemini|chat_completions|gem_resp_function_calls_parallel":   {Kind: "semantic", Reason: "S-TOOLID+S-ENVELOPE: tool-call ids missing in xir (Gemini source has no id), legacy synthesizes call_1/call_2; parallel calls now carried by both engines"},
	"response|gemini|chat_completions|gem_resp_multi_candidate":           {Kind: "semantic", Reason: "S-MULTICAND+S-ENVELOPE: xir keeps only the first Gemini candidate; legacy keeps all"},
	"response|gemini|chat_completions|gem_resp_sse_raw":                   {Kind: "category", Reason: "C-SSE: both reject SSE; legacy parse error (invalid) vs xir ErrStreamingConversion (same proxy 502)"},
	"response|gemini|chat_completions|gem_resp_text":                      {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|gemini|chat_completions|gem_resp_thought":                   {Kind: "decision", Reason: "D-SEALED-RESP: xir Strict rejects sealed/signed reasoning in responses; legacy drops it non-fatally"},
	"response|gemini|chat_completions|gem_resp_thought_signature":         {Kind: "decision", Reason: "D-SEALED-RESP: xir Strict rejects sealed/signed reasoning in responses; legacy drops it non-fatally"},
	"response|gemini|chat_completions|gem_resp_usage_cache_thoughts":      {Kind: "semantic", Reason: "S-ENVELOPE+S-USAGE-DETAIL: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|gemini|messages|gem_resp_empty_candidates":                  {Kind: "decision", Reason: "D-EXT-REQ: xir Strict fatals on unknown request/response fields via extensions.<proto> (legacy: non-fatal warning)"},
	"response|gemini|messages|gem_resp_error":                             {Kind: "semantic", Reason: "S-ERROR-SHELL+S-ENVELOPE: legacy converts error envelopes to empty shells; xir preserves the error member"},
	"response|gemini|messages|gem_resp_finish_max_tokens":                 {Kind: "semantic", Reason: "S-ENVELOPE+S-USAGE-ZERO: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|gemini|messages|gem_resp_finish_safety":                     {Kind: "semantic", Reason: "S-ENVELOPE+S-USAGE-ZERO: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|gemini|messages|gem_resp_function_call":                     {Kind: "semantic", Reason: "S-TOOLID+S-ENVELOPE+S-USAGE-ZERO: tool-call id empty in xir (Gemini source has no id), legacy synthesizes one; args now carried by both engines"},
	"response|gemini|messages|gem_resp_function_calls_parallel":           {Kind: "semantic", Reason: "S-TOOLID+S-ENVELOPE+S-USAGE-ZERO: tool-call ids empty in xir, legacy synthesizes distinct ids; parallel calls and args now carried by both engines"},
	"response|gemini|messages|gem_resp_multi_candidate":                   {Kind: "semantic", Reason: "S-ENVELOPE+S-USAGE-ZERO: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|gemini|messages|gem_resp_sse_raw":                           {Kind: "category", Reason: "C-SSE: both reject SSE; legacy parse error (invalid) vs xir ErrStreamingConversion (same proxy 502)"},
	"response|gemini|messages|gem_resp_text":                              {Kind: "semantic", Reason: "S-ENVELOPE+S-USAGE-ZERO: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|gemini|messages|gem_resp_thought":                           {Kind: "decision", Reason: "D-SEALED-RESP: xir Strict rejects sealed/signed reasoning in responses; legacy drops it non-fatally"},
	"response|gemini|messages|gem_resp_thought_signature":                 {Kind: "decision", Reason: "D-SEALED-RESP: xir Strict rejects sealed/signed reasoning in responses; legacy drops it non-fatally"},
	"response|gemini|messages|gem_resp_usage_cache_thoughts":              {Kind: "semantic", Reason: "S-ENVELOPE+S-USAGE-ZERO+S-USAGE-DETAIL: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|gemini|responses|gem_resp_empty_candidates":                 {Kind: "decision", Reason: "D-EXT-REQ: xir Strict fatals on unknown request/response fields via extensions.<proto> (legacy: non-fatal warning)"},
	"response|gemini|responses|gem_resp_error":                            {Kind: "semantic", Reason: "S-ENVELOPE+S-ERROR-SHELL: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|gemini|responses|gem_resp_finish_max_tokens":                {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|gemini|responses|gem_resp_finish_safety":                    {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|gemini|responses|gem_resp_function_call":                    {Kind: "semantic", Reason: "S-ENVELOPE+S-TOOLID: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|gemini|responses|gem_resp_function_calls_parallel":          {Kind: "semantic", Reason: "S-ENVELOPE+S-TOOLID: synthesized envelope members differ; xir omits call_id/item ids; parallel calls now carried by both engines"},
	"response|gemini|responses|gem_resp_multi_candidate":                  {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|gemini|responses|gem_resp_sse_raw":                          {Kind: "category", Reason: "C-SSE: both reject SSE; legacy parse error (invalid) vs xir ErrStreamingConversion (same proxy 502)"},
	"response|gemini|responses|gem_resp_text":                             {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|gemini|responses|gem_resp_thought":                          {Kind: "decision", Reason: "D-SEALED-RESP: xir Strict rejects sealed/signed reasoning in responses; legacy drops it non-fatally"},
	"response|gemini|responses|gem_resp_thought_signature":                {Kind: "decision", Reason: "D-SEALED-RESP: xir Strict rejects sealed/signed reasoning in responses; legacy drops it non-fatally"},
	"response|gemini|responses|gem_resp_usage_cache_thoughts":             {Kind: "semantic", Reason: "S-ENVELOPE+S-USAGE-DETAIL: synthesized envelope members differ; cache/reasoning usage details carried by xir only"},
	"response|messages|chat_completions|msg_resp_error":                   {Kind: "semantic", Reason: "S-ERROR-SHELL+S-ENVELOPE: legacy converts error envelopes to empty shells; xir preserves the error member"},
	"response|messages|chat_completions|msg_resp_sse_raw":                 {Kind: "category", Reason: "C-SSE: both reject SSE; legacy parse error (invalid) vs xir ErrStreamingConversion (same proxy 502)"},
	"response|messages|chat_completions|msg_resp_stop_max_tokens":         {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|messages|chat_completions|msg_resp_stop_refusal":            {Kind: "semantic", Reason: "S-REFUSAL+S-ENVELOPE: refusal/incomplete classification differs (finish_reason/status/promptFeedback)"},
	"response|messages|chat_completions|msg_resp_text":                    {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|messages|chat_completions|msg_resp_thinking":                {Kind: "decision", Reason: "D-SEALED-RESP: xir Strict rejects sealed/signed reasoning in responses; legacy drops it non-fatally"},
	"response|messages|chat_completions|msg_resp_tool_use":                {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|messages|chat_completions|msg_resp_tool_use_parallel":       {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|messages|chat_completions|msg_resp_usage_cache":             {Kind: "semantic", Reason: "S-ENVELOPE+S-USAGE-DETAIL: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|messages|gemini|msg_resp_error":                             {Kind: "semantic", Reason: "S-ERROR-SHELL: legacy converts error envelopes to empty shells; xir preserves the error member"},
	"response|messages|gemini|msg_resp_sse_raw":                           {Kind: "category", Reason: "C-SSE: both reject SSE; legacy parse error (invalid) vs xir ErrStreamingConversion (same proxy 502)"},
	"response|messages|gemini|msg_resp_stop_max_tokens":                   {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|messages|gemini|msg_resp_stop_refusal":                      {Kind: "semantic", Reason: "S-ENVELOPE+S-REFUSAL: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|messages|gemini|msg_resp_text":                              {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|messages|gemini|msg_resp_thinking":                          {Kind: "decision", Reason: "D-SEALED-RESP: xir Strict rejects sealed/signed reasoning in responses; legacy drops it non-fatally"},
	"response|messages|gemini|msg_resp_tool_use":                          {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|messages|gemini|msg_resp_tool_use_parallel":                 {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|messages|gemini|msg_resp_usage_cache":                       {Kind: "semantic", Reason: "S-ENVELOPE+S-USAGE-DETAIL: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|messages|responses|msg_resp_error":                          {Kind: "semantic", Reason: "S-ENVELOPE+S-ERROR-SHELL: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|messages|responses|msg_resp_sse_raw":                        {Kind: "category", Reason: "C-SSE: both reject SSE; legacy parse error (invalid) vs xir ErrStreamingConversion (same proxy 502)"},
	"response|messages|responses|msg_resp_stop_max_tokens":                {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|messages|responses|msg_resp_stop_refusal":                   {Kind: "semantic", Reason: "S-ENVELOPE+S-REFUSAL: synthesized envelope members differ; refusal/incomplete classification differs (status/incomplete_details)"},
	"response|messages|responses|msg_resp_text":                           {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|messages|responses|msg_resp_thinking":                       {Kind: "decision", Reason: "D-SEALED-RESP: xir Strict rejects sealed/signed reasoning in responses; legacy drops it non-fatally"},
	"response|messages|responses|msg_resp_tool_use":                       {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|messages|responses|msg_resp_tool_use_parallel":              {Kind: "semantic", Reason: "S-ENVELOPE: synthesized envelope members differ (id/created/created_at/modelVersion/responseId/item id/status/output_text)"},
	"response|messages|responses|msg_resp_usage_cache":                    {Kind: "semantic", Reason: "S-ENVELOPE+S-USAGE-ZERO+S-USAGE-DETAIL: synthesized envelope members differ; legacy zero-valued cache fields vs xir usage details"},
	"response|responses|chat_completions|resp_resp_error":                 {Kind: "semantic", Reason: "S-ERROR-SHELL+S-ENVELOPE: legacy converts error envelopes to empty shells; xir preserves the error member"},
	"response|responses|chat_completions|resp_resp_incomplete":            {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|chat_completions|resp_resp_reasoning_encrypted":   {Kind: "decision", Reason: "D-EXT-RESP+D-SEALED-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|chat_completions|resp_resp_reasoning_summary":     {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|chat_completions|resp_resp_refusal":               {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|chat_completions|resp_resp_sse_raw":               {Kind: "category", Reason: "C-SSE: both reject SSE; legacy parse error (invalid) vs xir ErrStreamingConversion (same proxy 502)"},
	"response|responses|chat_completions|resp_resp_text":                  {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|chat_completions|resp_resp_tool_call":             {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|chat_completions|resp_resp_tool_calls_parallel":   {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|chat_completions|resp_resp_usage_details":         {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|gemini|resp_resp_error":                           {Kind: "semantic", Reason: "S-ERROR-SHELL: legacy converts error envelopes to empty shells; xir preserves the error member"},
	"response|responses|gemini|resp_resp_incomplete":                      {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|gemini|resp_resp_reasoning_encrypted":             {Kind: "decision", Reason: "D-EXT-RESP+D-SEALED-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|gemini|resp_resp_reasoning_summary":               {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|gemini|resp_resp_refusal":                         {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|gemini|resp_resp_sse_raw":                         {Kind: "category", Reason: "C-SSE: both reject SSE; legacy parse error (invalid) vs xir ErrStreamingConversion (same proxy 502)"},
	"response|responses|gemini|resp_resp_text":                            {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|gemini|resp_resp_tool_call":                       {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|gemini|resp_resp_tool_calls_parallel":             {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|gemini|resp_resp_usage_details":                   {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|messages|resp_resp_error":                         {Kind: "semantic", Reason: "S-ERROR-SHELL+S-ENVELOPE: legacy converts error envelopes to empty shells; xir preserves the error member"},
	"response|responses|messages|resp_resp_incomplete":                    {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|messages|resp_resp_reasoning_encrypted":           {Kind: "decision", Reason: "D-EXT-RESP+D-SEALED-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|messages|resp_resp_reasoning_summary":             {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|messages|resp_resp_refusal":                       {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|messages|resp_resp_sse_raw":                       {Kind: "category", Reason: "C-SSE: both reject SSE; legacy parse error (invalid) vs xir ErrStreamingConversion (same proxy 502)"},
	"response|responses|messages|resp_resp_text":                          {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|messages|resp_resp_tool_call":                     {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|messages|resp_resp_tool_calls_parallel":           {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
	"response|responses|messages|resp_resp_usage_details":                 {Kind: "decision", Reason: "D-EXT-RESP: xir Strict fatals on unmodeled Responses envelope members (created_at) via extensions.responses"},
}

func TestParityKnownDivergences(t *testing.T) {
	if len(knownParityDivergences) == 0 {
		t.Skip("no known divergences registered")
	}
	requests := loadParityCorpus(t, "requests.json")
	responses := loadParityCorpus(t, "responses.json")
	byID := make(map[string]parityCase, len(requests)+len(responses))
	for _, c := range requests {
		byID["request|"+c.ID] = c
	}
	for _, c := range responses {
		byID["response|"+c.ID] = c
	}
	for key, entry := range knownParityDivergences {
		t.Run(key, func(t *testing.T) {
			// key = direction|from|to|caseID
			parts := strings.Split(key, "|")
			if len(parts) != 4 {
				t.Fatalf("malformed divergence key %q", key)
			}
			direction, from, to, caseID := parts[0], parts[1], parts[2], parts[3]
			c, ok := byID[direction+"|"+caseID]
			if !ok {
				t.Fatalf("divergence key %q references unknown case", key)
			}
			body := c.bytes()
			var legacy, xir parityOutcome
			if direction == "request" {
				legacy = runLegacyRequest(body, protocolconvert.Protocol(from), protocolconvert.Protocol(to), c.Vertex)
				xir = runXIRRequest(body, protocolconvert.Protocol(from), protocolconvert.Protocol(to), c.Vertex)
			} else {
				legacy = runLegacyResponse(body, protocolconvert.Protocol(from), protocolconvert.Protocol(to), c.Vertex)
				xir = runXIRResponse(body, protocolconvert.Protocol(from), protocolconvert.Protocol(to), c.Vertex)
			}
			verdict := compareParityOutcomes(legacy, xir)
			t.Logf("input:  %s\nlegacy: accepted=%v category=%s err=%q warnings=%v\n        body=%s\nxir:    accepted=%v category=%s err=%q losses=%v\n        body=%s",
				body,
				legacy.Accepted, legacy.Category, legacy.Err, legacy.Warnings, legacy.Body,
				xir.Accepted, xir.Category, xir.Err, xir.Warnings, xir.Body)
			reproduced := false
			switch entry.Kind {
			case "decision":
				reproduced = verdict.Class == "decision-divergence"
			case "semantic":
				reproduced = verdict.Class == "semantic-divergence"
			case "category":
				reproduced = verdict.Class == "category-divergence"
			default:
				t.Fatalf("unknown divergence kind %q", entry.Kind)
			}
			if !reproduced {
				t.Errorf("divergence %q (%s) no longer reproduces (verdict=%s): the engines converged; promote this cell into the resident parity corpus and update .scratch/audit/p5-parity.md", key, entry.Kind, verdict.Class)
			}
		})
	}
}
