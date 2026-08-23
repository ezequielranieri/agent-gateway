-- 0011_review_requests.down.sql
-- Drop review_requests table

DROP POLICY IF EXISTS review_requests_tenant_isolation ON public.review_requests;
DROP TABLE IF EXISTS public.review_requests;