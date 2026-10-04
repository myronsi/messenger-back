-- name: EnsureSecuritySettings :exec
INSERT INTO user_security_settings (user_id) VALUES (@user_id) ON CONFLICT (user_id) DO NOTHING;

-- name: GetSecuritySettings :one
SELECT * FROM user_security_settings WHERE user_id = @user_id;

-- name: SetSessionDuration :execrows
UPDATE user_security_settings SET session_duration_days = @days WHERE user_id = @user_id;

-- name: SetPendingTwoFactorSecret :execrows
UPDATE user_security_settings SET pending_two_factor_secret = @secret
WHERE user_id = @user_id AND NOT two_factor_enabled;

-- name: EnableTwoFactor :execrows
-- Compare-and-swap on the pending secret, so a second setup between verify and confirm cannot be confirmed by accident.
UPDATE user_security_settings
SET two_factor_enabled = TRUE,
    two_factor_secret = pending_two_factor_secret,
    pending_two_factor_secret = NULL
WHERE user_id = @user_id AND NOT two_factor_enabled AND pending_two_factor_secret = @pending_secret;

-- name: DisableTwoFactor :execrows
UPDATE user_security_settings
SET two_factor_enabled = FALSE, two_factor_secret = NULL, pending_two_factor_secret = NULL
WHERE user_id = @user_id;

-- name: InsertRecoveryCodes :exec
INSERT INTO user_2fa_recovery_codes (user_id, code_hash)
SELECT @user_id::bigint, unnest(@code_hashes::text[]);

-- name: DeleteRecoveryCodes :exec
DELETE FROM user_2fa_recovery_codes WHERE user_id = @user_id;

-- name: UseRecoveryCode :execrows
UPDATE user_2fa_recovery_codes SET used_at = now()
WHERE user_id = @user_id AND code_hash = @code_hash AND used_at IS NULL;

-- name: CountUnusedRecoveryCodes :one
SELECT count(*) FROM user_2fa_recovery_codes WHERE user_id = @user_id AND used_at IS NULL;