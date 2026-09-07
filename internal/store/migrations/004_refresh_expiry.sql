-- When re-authorisation becomes unavoidable.
--
-- The refresh token has its own lifetime, separate from the access token's.
-- Refreshing extends the access token indefinitely but cannot extend this: once
-- it passes, every refresh fails and only a human at a browser can fix it.
-- Claude Code reads the same value to show "Your login expires in N days ·
-- run /login to renew", and starts warning three days out.
--
-- NULL means the provider did not tell us, which is not the same as expired.
ALTER TABLE accounts ADD COLUMN refresh_expires_at TEXT;
