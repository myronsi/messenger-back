-- name: UpdatePasskeyForLogin :execrows
UPDATE user_passkeys SET credential = @credential, last_used_at = now()
WHERE credential_id = @credential_id AND user_id = @user_id AND credential = @expected_credential;
