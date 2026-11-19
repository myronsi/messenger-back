-- name: InsertUserReportOutcomes :many
INSERT INTO report_outcomes (reporter_id, subject_type, subject_id, outcome, created_at)
SELECT r.reporter_id, 'user', r.target_id, @outcome, @now::timestamptz
FROM user_reports r
WHERE r.target_id = @target_id AND r.withdrawn_at IS NULL AND r.dismissed_at IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM report_outcomes o
      WHERE o.reporter_id = r.reporter_id AND o.subject_type = 'user' AND o.subject_id = r.target_id AND o.created_at >= r.updated_at
  )
RETURNING id, reporter_id, subject_type, subject_id, outcome, created_at, read_at;

-- name: InsertDismissedReportOutcomes :many
INSERT INTO report_outcomes (reporter_id, subject_type, subject_id, outcome, created_at)
SELECT r.reporter_id, 'user', r.target_id, 'no_violation', @now::timestamptz
FROM user_reports r
WHERE r.target_id = @target_id AND r.reporter_id = ANY(@reporter_ids::BIGINT[])
  AND NOT EXISTS (
      SELECT 1 FROM report_outcomes o
      WHERE o.reporter_id = r.reporter_id AND o.subject_type = 'user' AND o.subject_id = r.target_id AND o.created_at >= r.updated_at
  )
RETURNING id, reporter_id, subject_type, subject_id, outcome, created_at, read_at;

-- name: InsertStickerPackReportOutcomes :many
INSERT INTO report_outcomes (reporter_id, subject_type, subject_id, outcome, created_at)
SELECT r.reporter_id, 'sticker_pack', r.pack_id, @outcome, @now::timestamptz
FROM sticker_pack_reports r
WHERE r.pack_id = @pack_id
  AND NOT EXISTS (
      SELECT 1 FROM report_outcomes o
      WHERE o.reporter_id = r.reporter_id AND o.subject_type = 'sticker_pack' AND o.subject_id = r.pack_id AND o.created_at >= r.created_at
  )
RETURNING id, reporter_id, subject_type, subject_id, outcome, created_at, read_at;

-- name: InsertEmojiPackReportOutcomes :many
INSERT INTO report_outcomes (reporter_id, subject_type, subject_id, outcome, created_at)
SELECT r.reporter_id, 'emoji_pack', r.pack_id, @outcome, @now::timestamptz
FROM emoji_pack_reports r
WHERE r.pack_id = @pack_id
  AND NOT EXISTS (
      SELECT 1 FROM report_outcomes o
      WHERE o.reporter_id = r.reporter_id AND o.subject_type = 'emoji_pack' AND o.subject_id = r.pack_id AND o.created_at >= r.created_at
  )
RETURNING id, reporter_id, subject_type, subject_id, outcome, created_at, read_at;

-- name: ListReportOutcomes :many
SELECT id, reporter_id, subject_type, subject_id, outcome, created_at, read_at
FROM report_outcomes
WHERE reporter_id = @reporter_id AND created_at >= @since
ORDER BY id DESC
LIMIT @max_rows;

-- name: MarkReportOutcomeRead :execrows
UPDATE report_outcomes SET read_at = @now::timestamptz
WHERE id = @id AND reporter_id = @reporter_id AND read_at IS NULL;

-- name: ReportOutcomeExists :one
SELECT EXISTS (SELECT 1 FROM report_outcomes WHERE id = @id AND reporter_id = @reporter_id);

-- name: DeleteReportOutcomesBefore :execrows
DELETE FROM report_outcomes WHERE created_at < @before;
