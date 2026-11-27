-- name: EnqueueMediaJob :execrows
INSERT INTO media_jobs (attachment_id, kind) VALUES (@attachment_id, @kind)
ON CONFLICT (attachment_id, kind) DO NOTHING;

-- name: ClaimMediaJob :one
UPDATE media_jobs
SET state = 'running', attempts = attempts + 1, started_at = now()
WHERE id = (
    SELECT q.id FROM media_jobs q
    WHERE q.state = 'queued' AND q.run_after <= now()
      AND (q.kind <> 'import_image' OR NOT EXISTS (
        SELECT 1 FROM media_jobs r WHERE r.kind = 'import_image' AND r.state = 'running'))
    ORDER BY q.kind = 'import_image', q.run_after, q.id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
RETURNING *;

-- name: FinishMediaJob :exec
UPDATE media_jobs SET state = 'done', error = NULL, finished_at = now() WHERE id = @id;

-- name: RetryMediaJob :exec
UPDATE media_jobs SET state = 'queued', error = @error, run_after = @run_after, started_at = NULL WHERE id = @id;

-- name: FailMediaJob :exec
UPDATE media_jobs SET state = 'failed', error = @error, finished_at = now() WHERE id = @id;

-- name: RequeueStuckMediaJobs :execrows
UPDATE media_jobs SET state = 'queued', started_at = NULL, run_after = now()
WHERE state = 'running' AND started_at < @started_before;

-- name: CountMediaJobs :many
SELECT state, count(*)::bigint AS n FROM media_jobs GROUP BY state;

-- name: DeleteFinishedMediaJobs :execrows
DELETE FROM media_jobs WHERE state = 'done' AND finished_at < @finished_before;
