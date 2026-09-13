-- schema-hash: e41f911f71a945d66ef4e34a66cc2ca92fd000d31e4558c5178a7f0ca63511d1
-- Keep earliest-validation selection local to one event as retained history grows.
CREATE INDEX IF NOT EXISTS video_asset_validations_event
    ON video_asset_validations (event_id, asset_id, recorded_at, id);
