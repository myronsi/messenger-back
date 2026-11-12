ALTER TABLE users ADD COLUMN profile_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE sticker_packs ADD COLUMN version BIGINT NOT NULL DEFAULT 0;

CREATE FUNCTION bump_profile_version() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.username IS DISTINCT FROM OLD.username
        OR NEW.display_name IS DISTINCT FROM OLD.display_name
        OR NEW.bio IS DISTINCT FROM OLD.bio
        OR NEW.avatar_url IS DISTINCT FROM OLD.avatar_url
        OR NEW.avatar_attachment_id IS DISTINCT FROM OLD.avatar_attachment_id THEN
        NEW.profile_version = OLD.profile_version + 1;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER users_bump_profile_version BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION bump_profile_version();

CREATE FUNCTION bump_sticker_pack_title_version() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.title IS DISTINCT FROM OLD.title
        OR NEW.hidden_at IS DISTINCT FROM OLD.hidden_at
        OR NEW.deleted_at IS DISTINCT FROM OLD.deleted_at THEN
        NEW.version = OLD.version + 1;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER sticker_packs_bump_version BEFORE UPDATE ON sticker_packs
    FOR EACH ROW EXECUTE FUNCTION bump_sticker_pack_title_version();

CREATE FUNCTION bump_sticker_pack_version() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    UPDATE sticker_packs SET version = version + 1 WHERE id = COALESCE(NEW.pack_id, OLD.pack_id);
    RETURN NULL;
END;
$$;

CREATE TRIGGER stickers_bump_pack_version AFTER INSERT OR UPDATE OR DELETE ON stickers
    FOR EACH ROW EXECUTE FUNCTION bump_sticker_pack_version();
