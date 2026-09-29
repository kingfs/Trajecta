-- Drop the distinct-value override on "semantic_nodes"."trace_id".
ALTER TABLE "semantic_nodes" ALTER COLUMN "trace_id" RESET (n_distinct);
