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
// this registry into resident parity assertions.
//
// B1-B4 milestone: the Responses-envelope, Messages-max_tokens,
// loss-ledger/stream-spelling and Gemini tool-id/multi-candidate blockers were
// fixed, so every remaining "decision" entry is xir accepting traffic the
// legacy engine rejected (a capability expansion, never the reverse), and the
// remaining "semantic" entries are representation differences between two
// valid wire bodies. The registry therefore no longer contains a cell where
// xir refuses traffic the legacy default served.

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
	"request|chat_completions|gemini|chat_functions_legacy":               {Kind: "decision", Reason: "D-MAP-IMPROVE: xir accepts where legacy rejects (legacy=fatal)"},
	"request|chat_completions|gemini|chat_image_data_uri":                 {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|chat_completions|gemini|chat_image_url":                      {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|chat_completions|gemini|chat_input_audio":                    {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|chat_completions|gemini|chat_n2":                             {Kind: "decision", Reason: "D-EXPAND: xir accepts where legacy rejects (legacy=fatal); capability expansion recorded by the loss ledger"},
	"request|chat_completions|gemini|chat_presence_penalty":               {Kind: "decision", Reason: "D-EXPAND: xir accepts where legacy rejects (legacy=fatal); capability expansion recorded by the loss ledger"},
	"request|chat_completions|gemini|chat_reasoning_effort":               {Kind: "decision", Reason: "D-EXPAND: xir accepts where legacy rejects (legacy=fatal); capability expansion recorded by the loss ledger"},
	"request|chat_completions|gemini|chat_reasoning_history":              {Kind: "decision", Reason: "D-REASON-HIST: xir accepts where legacy rejects (legacy=invalid)"},
	"request|chat_completions|gemini|chat_seed":                           {Kind: "decision", Reason: "D-EXPAND: xir accepts where legacy rejects (legacy=fatal); capability expansion recorded by the loss ledger"},
	"request|chat_completions|gemini|chat_tool_choice_unknown":            {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|chat_completions|gemini|chat_tool_strict":                    {Kind: "decision", Reason: "D-STRICT: xir accepts where legacy rejects (legacy=fatal)"},
	"request|chat_completions|gemini|chat_top_k":                          {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at generationConfig"},
	"request|chat_completions|messages|chat_developer_role":               {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|chat_completions|messages|chat_functions_legacy":             {Kind: "decision", Reason: "D-MAP-IMPROVE: xir accepts where legacy rejects (legacy=fatal)"},
	"request|chat_completions|messages|chat_image_data_uri":               {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|chat_completions|messages|chat_image_url":                    {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|chat_completions|messages|chat_input_audio":                  {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|chat_completions|messages|chat_max_completion_tokens":        {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|chat_completions|messages|chat_max_tokens":                   {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|chat_completions|messages|chat_multiturn":                    {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content (+2 more)"},
	"request|chat_completions|messages|chat_reasoning_history":            {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|chat_completions|messages|chat_sampling":                     {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|chat_completions|messages|chat_system_blocks":                {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|chat_completions|messages|chat_text_plain":                   {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|chat_completions|messages|chat_tool_choice_auto":             {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|chat_completions|messages|chat_tool_choice_empty":            {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|chat_completions|messages|chat_tool_choice_named":            {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|chat_completions|messages|chat_tool_choice_none":             {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|chat_completions|messages|chat_tool_choice_required":         {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|chat_completions|messages|chat_tool_choice_unknown":          {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|chat_completions|messages|chat_tool_loop":                    {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|chat_completions|messages|chat_tool_loop_parallel":           {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|chat_completions|messages|chat_tool_strict":                  {Kind: "decision", Reason: "D-STRICT: xir accepts where legacy rejects (legacy=fatal)"},
	"request|chat_completions|messages|chat_tools":                        {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|chat_completions|messages|chat_top_k":                        {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at top_k (+1 more)"},
	"request|chat_completions|messages|chat_unknown_field":                {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|chat_completions|messages|chat_user_metadata":                {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at metadata.user_id (+2 more)"},
	"request|chat_completions|messages|chat_vertex_text":                  {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|chat_completions|responses|chat_developer_role":              {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at instructions (+1 more)"},
	"request|chat_completions|responses|chat_functions_legacy":            {Kind: "decision", Reason: "D-MAP-IMPROVE: xir accepts where legacy rejects (legacy=fatal)"},
	"request|chat_completions|responses|chat_image_data_uri":              {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|chat_completions|responses|chat_image_url":                   {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|chat_completions|responses|chat_input_audio":                 {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|chat_completions|responses|chat_multiturn":                   {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at instructions (+1 more)"},
	"request|chat_completions|responses|chat_parallel_tool_calls_false":   {Kind: "decision", Reason: "D-PARALLEL: xir accepts where legacy rejects (legacy=fatal)"},
	"request|chat_completions|responses|chat_reasoning_effort":            {Kind: "decision", Reason: "D-EXPAND: xir accepts where legacy rejects (legacy=fatal); capability expansion recorded by the loss ledger"},
	"request|chat_completions|responses|chat_reasoning_history":           {Kind: "decision", Reason: "D-REASON-HIST: xir accepts where legacy rejects (legacy=invalid)"},
	"request|chat_completions|responses|chat_response_format":             {Kind: "decision", Reason: "D-EXPAND: xir accepts where legacy rejects (legacy=fatal); capability expansion recorded by the loss ledger"},
	"request|chat_completions|responses|chat_system_blocks":               {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at instructions (+1 more)"},
	"request|chat_completions|responses|chat_tool_choice_unknown":         {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|chat_completions|responses|chat_tool_loop":                   {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at input[2].name"},
	"request|chat_completions|responses|chat_tool_loop_parallel":          {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at input[3].name (+1 more)"},
	"request|chat_completions|responses|chat_user_metadata":               {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at metadata"},
	"request|gemini|chat_completions|gem_file_data":                       {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|gemini|chat_completions|gem_inline_data":                     {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|gemini|chat_completions|gem_response_schema":                 {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|gemini|chat_completions|gem_safety_settings":                 {Kind: "decision", Reason: "D-EXT-REQ: xir accepts where legacy rejects (legacy=fatal)"},
	"request|gemini|chat_completions|gem_sampling":                        {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at stop"},
	"request|gemini|chat_completions|gem_stream_flag":                     {Kind: "decision", Reason: "D-GEM-STREAM: xir accepts where legacy rejects (legacy=streaming)"},
	"request|gemini|chat_completions|gem_thinking_config":                 {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|gemini|chat_completions|gem_thought_signature":               {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|gemini|chat_completions|gem_tool_loop":                       {Kind: "decision", Reason: "D-EXPAND: xir accepts where legacy rejects (legacy=invalid); capability expansion recorded by the loss ledger"},
	"request|gemini|chat_completions|gem_tool_loop_parallel":              {Kind: "decision", Reason: "D-EXPAND: xir accepts where legacy rejects (legacy=invalid); capability expansion recorded by the loss ledger"},
	"request|gemini|messages|gem_file_data":                               {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|gemini|messages|gem_inline_data":                             {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|gemini|messages|gem_max_output_tokens":                       {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|gemini|messages|gem_multiturn":                               {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content (+2 more)"},
	"request|gemini|messages|gem_response_schema":                         {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|gemini|messages|gem_safety_settings":                         {Kind: "decision", Reason: "D-EXT-REQ: xir accepts where legacy rejects (legacy=fatal)"},
	"request|gemini|messages|gem_sampling":                                {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|gemini|messages|gem_stream_flag":                             {Kind: "decision", Reason: "D-GEM-STREAM: xir accepts where legacy rejects (legacy=streaming)"},
	"request|gemini|messages|gem_system_instruction":                      {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|gemini|messages|gem_text_plain":                              {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|gemini|messages|gem_thinking_config":                         {Kind: "decision", Reason: "D-MAP-IMPROVE: xir accepts where legacy rejects (legacy=invalid)"},
	"request|gemini|messages|gem_thought_signature":                       {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|gemini|messages|gem_tool_choice_any":                         {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|gemini|messages|gem_tool_choice_auto":                        {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|gemini|messages|gem_tool_choice_named":                       {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|gemini|messages|gem_tool_choice_none":                        {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|gemini|messages|gem_tool_loop":                               {Kind: "decision", Reason: "D-EXPAND: xir accepts where legacy rejects (legacy=invalid); capability expansion recorded by the loss ledger"},
	"request|gemini|messages|gem_tool_loop_parallel":                      {Kind: "decision", Reason: "D-EXPAND: xir accepts where legacy rejects (legacy=invalid); capability expansion recorded by the loss ledger"},
	"request|gemini|messages|gem_tools":                                   {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|gemini|messages|gem_unknown_field":                           {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|gemini|messages|gem_vertex_text":                             {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|gemini|responses|gem_file_data":                              {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|gemini|responses|gem_inline_data":                            {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|gemini|responses|gem_response_schema":                        {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|gemini|responses|gem_safety_settings":                        {Kind: "decision", Reason: "D-EXT-REQ: xir accepts where legacy rejects (legacy=fatal)"},
	"request|gemini|responses|gem_stream_flag":                            {Kind: "decision", Reason: "D-GEM-STREAM: xir accepts where legacy rejects (legacy=streaming)"},
	"request|gemini|responses|gem_thinking_config":                        {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|gemini|responses|gem_thought_signature":                      {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|gemini|responses|gem_tool_loop":                              {Kind: "decision", Reason: "D-EXPAND: xir accepts where legacy rejects (legacy=invalid); capability expansion recorded by the loss ledger"},
	"request|gemini|responses|gem_tool_loop_parallel":                     {Kind: "decision", Reason: "D-EXPAND: xir accepts where legacy rejects (legacy=invalid); capability expansion recorded by the loss ledger"},
	"request|messages|chat_completions|msg_image":                         {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|messages|chat_completions|msg_metadata":                      {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at metadata"},
	"request|messages|chat_completions|msg_redacted_thinking":             {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|messages|chat_completions|msg_sampling":                      {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at stop"},
	"request|messages|chat_completions|msg_thinking":                      {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|messages|chat_completions|msg_tool_result_blocks":            {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[1].content"},
	"request|messages|gemini|msg_image":                                   {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|messages|gemini|msg_redacted_thinking":                       {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|messages|gemini|msg_thinking":                                {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|messages|gemini|msg_thinking_param":                          {Kind: "decision", Reason: "D-MAP-IMPROVE: xir accepts where legacy rejects (legacy=fatal)"},
	"request|messages|responses|msg_image":                                {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|messages|responses|msg_metadata":                             {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at metadata"},
	"request|messages|responses|msg_redacted_thinking":                    {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|messages|responses|msg_thinking":                             {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|messages|responses|msg_tool_result_blocks":                   {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at input[1].output"},
	"request|responses|chat_completions|resp_background_true":             {Kind: "decision", Reason: "D-EXT-REQ: xir accepts where legacy rejects (legacy=fatal)"},
	"request|responses|chat_completions|resp_hosted_tool":                 {Kind: "decision", Reason: "D-HOSTED: xir accepts where legacy rejects (legacy=invalid)"},
	"request|responses|chat_completions|resp_input_image":                 {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|responses|chat_completions|resp_previous_response_id":        {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|responses|chat_completions|resp_reasoning_effort":            {Kind: "decision", Reason: "D-EXPAND: xir accepts where legacy rejects (legacy=fatal); capability expansion recorded by the loss ledger"},
	"request|responses|chat_completions|resp_reasoning_encrypted":         {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|responses|chat_completions|resp_store_cache_key":             {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at prompt_cache_key (+1 more)"},
	"request|responses|chat_completions|resp_text_format":                 {Kind: "decision", Reason: "D-EXPAND: xir accepts where legacy rejects (legacy=fatal); capability expansion recorded by the loss ledger"},
	"request|responses|chat_completions|resp_text_format_default":         {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at response_format"},
	"request|responses|gemini|resp_background_true":                       {Kind: "decision", Reason: "D-EXT-REQ: xir accepts where legacy rejects (legacy=fatal)"},
	"request|responses|gemini|resp_hosted_tool":                           {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|responses|gemini|resp_input_image":                           {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|responses|gemini|resp_previous_response_id":                  {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|responses|gemini|resp_reasoning_effort":                      {Kind: "decision", Reason: "D-EXPAND: xir accepts where legacy rejects (legacy=fatal); capability expansion recorded by the loss ledger"},
	"request|responses|gemini|resp_reasoning_encrypted":                   {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|responses|gemini|resp_tool_result_structured":                {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at contents[1].parts[0].functionResponse.response.result (+1 more)"},
	"request|responses|messages|resp_background_true":                     {Kind: "decision", Reason: "D-EXT-REQ: xir accepts where legacy rejects (legacy=fatal)"},
	"request|responses|messages|resp_hosted_tool":                         {Kind: "decision", Reason: "D-HOSTED: xir accepts where legacy rejects (legacy=invalid)"},
	"request|responses|messages|resp_input_image":                         {Kind: "decision", Reason: "D-MEDIA: xir accepts where legacy rejects (legacy=invalid)"},
	"request|responses|messages|resp_instructions":                        {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|responses|messages|resp_max_output_tokens":                   {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|responses|messages|resp_multiturn_items":                     {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content (+2 more)"},
	"request|responses|messages|resp_previous_response_id":                {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|responses|messages|resp_reasoning_encrypted":                 {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=fatal; proxy-visible status is the same"},
	"request|responses|messages|resp_sampling":                            {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|responses|messages|resp_store_cache_key":                     {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|responses|messages|resp_text_format_default":                 {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|responses|messages|resp_text_plain":                          {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|responses|messages|resp_tool_choice_auto":                    {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|responses|messages|resp_tool_choice_named":                   {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|responses|messages|resp_tool_choice_none":                    {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|responses|messages|resp_tool_choice_required":                {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|responses|messages|resp_tool_loop":                           {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|responses|messages|resp_tools":                               {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"request|responses|messages|resp_unknown_field":                       {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at messages[0].content"},
	"response|chat_completions|gemini|chat_resp_empty_choices":            {Kind: "decision", Reason: "D-REGRESS: xir rejects where legacy accepts (xir=invalid: xir/convert: decode chat_completions response: chat: response choices array is empty)"},
	"response|chat_completions|gemini|chat_resp_error":                    {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at candidates (+1 more)"},
	"response|chat_completions|gemini|chat_resp_finish_content_filter":    {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at responseId (+2 more)"},
	"response|chat_completions|gemini|chat_resp_finish_length":            {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at responseId (+1 more)"},
	"response|chat_completions|gemini|chat_resp_logprobs":                 {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at responseId (+1 more)"},
	"response|chat_completions|gemini|chat_resp_reasoning":                {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at responseId (+2 more)"},
	"response|chat_completions|gemini|chat_resp_refusal":                  {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at responseId (+2 more)"},
	"response|chat_completions|gemini|chat_resp_sse_raw":                  {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=streaming; proxy-visible status is the same"},
	"response|chat_completions|gemini|chat_resp_text":                     {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at modelVersion (+1 more)"},
	"response|chat_completions|gemini|chat_resp_tool_call":                {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at modelVersion (+1 more)"},
	"response|chat_completions|gemini|chat_resp_tool_calls_parallel":      {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at modelVersion (+1 more)"},
	"response|chat_completions|gemini|chat_resp_usage_details":            {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at modelVersion (+3 more)"},
	"response|chat_completions|messages|chat_resp_empty_choices":          {Kind: "decision", Reason: "D-REGRESS: xir rejects where legacy accepts (xir=invalid: xir/convert: decode chat_completions response: chat: response choices array is empty)"},
	"response|chat_completions|messages|chat_resp_error":                  {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at stop_reason (+6 more)"},
	"response|chat_completions|messages|chat_resp_finish_content_filter":  {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at content.len"},
	"response|chat_completions|messages|chat_resp_reasoning":              {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at content.len"},
	"response|chat_completions|messages|chat_resp_sse_raw":                {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=streaming; proxy-visible status is the same"},
	"response|chat_completions|messages|chat_resp_text":                   {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at usage.cache_read_input_tokens (+1 more)"},
	"response|chat_completions|messages|chat_resp_tool_call":              {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at usage.cache_read_input_tokens (+1 more)"},
	"response|chat_completions|messages|chat_resp_tool_calls_parallel":    {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at usage.cache_read_input_tokens (+1 more)"},
	"response|chat_completions|messages|chat_resp_usage_details":          {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at usage.cache_creation_input_tokens (+1 more)"},
	"response|chat_completions|responses|chat_resp_empty_choices":         {Kind: "decision", Reason: "D-REGRESS: xir rejects where legacy accepts (xir=invalid: xir/convert: decode chat_completions response: chat: response choices array is empty)"},
	"response|chat_completions|responses|chat_resp_error":                 {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+2 more)"},
	"response|chat_completions|responses|chat_resp_finish_content_filter": {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+1 more)"},
	"response|chat_completions|responses|chat_resp_finish_length":         {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+3 more)"},
	"response|chat_completions|responses|chat_resp_logprobs":              {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+3 more)"},
	"response|chat_completions|responses|chat_resp_reasoning":             {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+2 more)"},
	"response|chat_completions|responses|chat_resp_refusal":               {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+2 more)"},
	"response|chat_completions|responses|chat_resp_sse_raw":               {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=streaming; proxy-visible status is the same"},
	"response|chat_completions|responses|chat_resp_text":                  {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+3 more)"},
	"response|chat_completions|responses|chat_resp_tool_call":             {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+2 more)"},
	"response|chat_completions|responses|chat_resp_tool_calls_parallel":   {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+7 more)"},
	"response|chat_completions|responses|chat_resp_usage_details":         {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+5 more)"},
	"response|gemini|chat_completions|gem_resp_empty_candidates":          {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+2 more)"},
	"response|gemini|chat_completions|gem_resp_error":                     {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at model (+5 more)"},
	"response|gemini|chat_completions|gem_resp_finish_max_tokens":         {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+1 more)"},
	"response|gemini|chat_completions|gem_resp_finish_safety":             {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+1 more)"},
	"response|gemini|chat_completions|gem_resp_function_call":             {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+1 more)"},
	"response|gemini|chat_completions|gem_resp_function_calls_parallel":   {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+1 more)"},
	"response|gemini|chat_completions|gem_resp_multi_candidate":           {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+1 more)"},
	"response|gemini|chat_completions|gem_resp_sse_raw":                   {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=streaming; proxy-visible status is the same"},
	"response|gemini|chat_completions|gem_resp_text":                      {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+1 more)"},
	"response|gemini|chat_completions|gem_resp_thought":                   {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+1 more)"},
	"response|gemini|chat_completions|gem_resp_thought_signature":         {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+2 more)"},
	"response|gemini|chat_completions|gem_resp_usage_cache_thoughts":      {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+3 more)"},
	"response|gemini|messages|gem_resp_empty_candidates":                  {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+3 more)"},
	"response|gemini|messages|gem_resp_error":                             {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at stop_reason (+6 more)"},
	"response|gemini|messages|gem_resp_finish_max_tokens":                 {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+2 more)"},
	"response|gemini|messages|gem_resp_finish_safety":                     {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+2 more)"},
	"response|gemini|messages|gem_resp_function_call":                     {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+3 more)"},
	"response|gemini|messages|gem_resp_function_calls_parallel":           {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+4 more)"},
	"response|gemini|messages|gem_resp_multi_candidate":                   {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+2 more)"},
	"response|gemini|messages|gem_resp_sse_raw":                           {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=streaming; proxy-visible status is the same"},
	"response|gemini|messages|gem_resp_text":                              {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+2 more)"},
	"response|gemini|messages|gem_resp_thought":                           {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+3 more)"},
	"response|gemini|messages|gem_resp_thought_signature":                 {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+5 more)"},
	"response|gemini|messages|gem_resp_usage_cache_thoughts":              {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at id (+2 more)"},
	"response|gemini|responses|gem_resp_empty_candidates":                 {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at incomplete_details (+6 more)"},
	"response|gemini|responses|gem_resp_error":                            {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+3 more)"},
	"response|gemini|responses|gem_resp_finish_max_tokens":                {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+4 more)"},
	"response|gemini|responses|gem_resp_finish_safety":                    {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+4 more)"},
	"response|gemini|responses|gem_resp_function_call":                    {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+3 more)"},
	"response|gemini|responses|gem_resp_function_calls_parallel":          {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+8 more)"},
	"response|gemini|responses|gem_resp_multi_candidate":                  {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+4 more)"},
	"response|gemini|responses|gem_resp_sse_raw":                          {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=streaming; proxy-visible status is the same"},
	"response|gemini|responses|gem_resp_text":                             {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+4 more)"},
	"response|gemini|responses|gem_resp_thought":                          {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+3 more)"},
	"response|gemini|responses|gem_resp_thought_signature":                {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+7 more)"},
	"response|gemini|responses|gem_resp_usage_cache_thoughts":             {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+6 more)"},
	"response|messages|chat_completions|msg_resp_error":                   {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at model (+5 more)"},
	"response|messages|chat_completions|msg_resp_sse_raw":                 {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=streaming; proxy-visible status is the same"},
	"response|messages|chat_completions|msg_resp_stop_max_tokens":         {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created"},
	"response|messages|chat_completions|msg_resp_stop_refusal":            {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created"},
	"response|messages|chat_completions|msg_resp_text":                    {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created"},
	"response|messages|chat_completions|msg_resp_thinking":                {Kind: "semantic", Reason: "D-MAP-IMPROVE: S-REPR: both accept; representation differs at created"},
	"response|messages|chat_completions|msg_resp_tool_use":                {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created"},
	"response|messages|chat_completions|msg_resp_tool_use_parallel":       {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created"},
	"response|messages|chat_completions|msg_resp_usage_cache":             {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created (+1 more)"},
	"response|messages|gemini|msg_resp_error":                             {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at candidates (+1 more)"},
	"response|messages|gemini|msg_resp_sse_raw":                           {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=streaming; proxy-visible status is the same"},
	"response|messages|gemini|msg_resp_stop_max_tokens":                   {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at modelVersion (+1 more)"},
	"response|messages|gemini|msg_resp_stop_refusal":                      {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at promptFeedback (+2 more)"},
	"response|messages|gemini|msg_resp_text":                              {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at modelVersion (+1 more)"},
	"response|messages|gemini|msg_resp_thinking":                          {Kind: "semantic", Reason: "D-MAP-IMPROVE: S-REPR: both accept; representation differs at modelVersion (+2 more)"},
	"response|messages|gemini|msg_resp_tool_use":                          {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at modelVersion (+1 more)"},
	"response|messages|gemini|msg_resp_tool_use_parallel":                 {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at modelVersion (+1 more)"},
	"response|messages|gemini|msg_resp_usage_cache":                       {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at modelVersion (+2 more)"},
	"response|messages|responses|msg_resp_error":                          {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+3 more)"},
	"response|messages|responses|msg_resp_sse_raw":                        {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=streaming; proxy-visible status is the same"},
	"response|messages|responses|msg_resp_stop_max_tokens":                {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+3 more)"},
	"response|messages|responses|msg_resp_stop_refusal":                   {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at incomplete_details (+5 more)"},
	"response|messages|responses|msg_resp_text":                           {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+3 more)"},
	"response|messages|responses|msg_resp_thinking":                       {Kind: "semantic", Reason: "D-MAP-IMPROVE: S-REPR: both accept; representation differs at created_at (+2 more)"},
	"response|messages|responses|msg_resp_tool_use":                       {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+5 more)"},
	"response|messages|responses|msg_resp_tool_use_parallel":              {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+4 more)"},
	"response|messages|responses|msg_resp_usage_cache":                    {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created_at (+6 more)"},
	"response|responses|chat_completions|resp_resp_error":                 {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at model (+5 more)"},
	"response|responses|chat_completions|resp_resp_incomplete":            {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created (+1 more)"},
	"response|responses|chat_completions|resp_resp_reasoning_encrypted":   {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created"},
	"response|responses|chat_completions|resp_resp_reasoning_summary":     {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created"},
	"response|responses|chat_completions|resp_resp_refusal":               {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created (+1 more)"},
	"response|responses|chat_completions|resp_resp_sse_raw":               {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=streaming; proxy-visible status is the same"},
	"response|responses|chat_completions|resp_resp_text":                  {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created"},
	"response|responses|chat_completions|resp_resp_tool_call":             {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created"},
	"response|responses|chat_completions|resp_resp_tool_calls_parallel":   {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created"},
	"response|responses|chat_completions|resp_resp_usage_details":         {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at created (+2 more)"},
	"response|responses|gemini|resp_resp_error":                           {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at candidates (+1 more)"},
	"response|responses|gemini|resp_resp_incomplete":                      {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at responseId (+2 more)"},
	"response|responses|gemini|resp_resp_reasoning_encrypted":             {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at responseId (+1 more)"},
	"response|responses|gemini|resp_resp_reasoning_summary":               {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at responseId (+2 more)"},
	"response|responses|gemini|resp_resp_refusal":                         {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at responseId (+3 more)"},
	"response|responses|gemini|resp_resp_sse_raw":                         {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=streaming; proxy-visible status is the same"},
	"response|responses|gemini|resp_resp_text":                            {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at modelVersion (+1 more)"},
	"response|responses|gemini|resp_resp_tool_call":                       {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at modelVersion (+1 more)"},
	"response|responses|gemini|resp_resp_tool_calls_parallel":             {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at modelVersion (+1 more)"},
	"response|responses|gemini|resp_resp_usage_details":                   {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at modelVersion (+3 more)"},
	"response|responses|messages|resp_resp_error":                         {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at stop_reason (+6 more)"},
	"response|responses|messages|resp_resp_incomplete":                    {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at stop_reason"},
	"response|responses|messages|resp_resp_reasoning_summary":             {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at content.len"},
	"response|responses|messages|resp_resp_refusal":                       {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at stop_reason"},
	"response|responses|messages|resp_resp_sse_raw":                       {Kind: "category", Reason: "C-CATEGORY: both reject; legacy=invalid xir=streaming; proxy-visible status is the same"},
	"response|responses|messages|resp_resp_text":                          {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at usage.cache_read_input_tokens (+1 more)"},
	"response|responses|messages|resp_resp_tool_call":                     {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at usage.cache_read_input_tokens (+1 more)"},
	"response|responses|messages|resp_resp_tool_calls_parallel":           {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at usage.cache_read_input_tokens (+1 more)"},
	"response|responses|messages|resp_resp_usage_details":                 {Kind: "semantic", Reason: "S-REPR: both accept; representation differs at usage.cache_creation_input_tokens (+1 more)"},
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
