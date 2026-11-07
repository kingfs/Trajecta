-- modify "channel_configs" table
ALTER TABLE "channel_configs" ADD COLUMN "proxy_json" character varying NOT NULL DEFAULT '{}';
