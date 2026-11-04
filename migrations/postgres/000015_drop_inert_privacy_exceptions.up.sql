DELETE FROM user_privacy_exceptions e
USING users u
LEFT JOIN user_privacy_settings p ON p.user_id = u.id
WHERE e.owner_id = u.id
  AND e.setting_key IN ('avatar_visibility', 'profile_visibility', 'presence_visibility')
  AND NOT (
    CASE e.setting_key
      WHEN 'avatar_visibility' THEN COALESCE(p.avatar_visibility, 'everyone')
      WHEN 'profile_visibility' THEN COALESCE(p.profile_visibility, 'everyone')
      ELSE COALESCE(p.presence_visibility, 'everyone')
    END
    = CASE e.effect WHEN 'deny' THEN 'everyone_except' ELSE 'nobody_except' END
  );
