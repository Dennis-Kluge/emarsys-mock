-- A fault-injected 429 has to be indistinguishable from a real one, or a client
-- whose backoff reads Retry-After never exercises that path -- which is the
-- whole reason to inject the 429 in the first place.
ALTER TABLE fault_rules ADD COLUMN retry_after INTEGER;
