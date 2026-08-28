-- name: CreateReviewRequest :one
INSERT INTO public.review_requests (tenant_id, requester_id, reviewer_id, action, payload, token_hash, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, tenant_id, requester_id, reviewer_id, action, payload, status, token_hash, expires_at, decided_at, decided_by, decision_reason, created_at, updated_at;

-- name: GetReviewRequestByTokenHash :one
SELECT id, tenant_id, requester_id, reviewer_id, payload, status, token_hash, expires_at, decided_at, decided_by, decision_reason, created_at, updated_at
FROM public.review_requests
WHERE token_hash = $1
LIMIT 1;

-- name: GetReviewRequestByID :one
SELECT id, tenant_id, requester_id, reviewer_id, action, payload, status, token_hash, expires_at, decided_at, decided_by, decision_reason, created_at, updated_at
FROM public.review_requests
WHERE tenant_id = $1 AND id = $2
LIMIT 1;

-- name: ApproveReviewRequest :one
UPDATE public.review_requests
SET status = 'APPROVED', decided_at = now(), decided_by = $3, decision_reason = $4, updated_at = now()
WHERE tenant_id = $1 AND id = $2 AND status = 'PENDING' AND expires_at > now()
RETURNING id, tenant_id, requester_id, reviewer_id, payload, status, token_hash, expires_at, decided_at, decided_by, decision_reason, created_at, updated_at;

-- name: RejectReviewRequest :one
UPDATE public.review_requests
SET status = 'REJECTED', decided_at = now(), decided_by = $3, decision_reason = $4, updated_at = now()
WHERE tenant_id = $1 AND id = $2 AND status = 'PENDING' AND expires_at > now()
RETURNING id, tenant_id, requester_id, reviewer_id, payload, status, token_hash, expires_at, decided_at, decided_by, decision_reason, created_at, updated_at;

-- name: ListReviewRequests :many
SELECT id, tenant_id, requester_id, reviewer_id, payload, status, token_hash, expires_at, decided_at, decided_by, decision_reason, created_at, updated_at
FROM public.review_requests
WHERE tenant_id = $1
  AND ($2::text IS NULL OR status = $2)
  AND ($3::uuid IS NULL OR requester_id = $3)
  AND ($4::uuid IS NULL OR reviewer_id = $4)
ORDER BY created_at DESC
LIMIT $5 OFFSET $6;

-- name: SweepExpiredReviews :exec
UPDATE public.review_requests
SET status = 'EXPIRED', updated_at = now()
WHERE tenant_id = $1 AND status = 'PENDING' AND expires_at <= now();