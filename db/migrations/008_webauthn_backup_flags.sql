-- Add backup_eligible and backup_state columns for WebAuthn credential flags
-- These are required by the go-webauthn library to validate credentials properly
ALTER TABLE webauthn_credentials ADD COLUMN backup_eligible INTEGER NOT NULL DEFAULT 0;
ALTER TABLE webauthn_credentials ADD COLUMN backup_state INTEGER NOT NULL DEFAULT 0;
