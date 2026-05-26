-- Story 5.3 — Atomic 3-axis rate-limit check + INCR + EXPIRE NX.
--
-- KEYS[1] = ratelimit:key:{api_key_id}:qps
-- KEYS[2] = ratelimit:key:{api_key_id}:rpm
-- KEYS[3] = ratelimit:key:{api_key_id}:tpm
-- ARGV[1] = qps_max  (int, ceiling)
-- ARGV[2] = rpm_max  (int, ceiling)
-- ARGV[3] = tpm_max  (int, ceiling)
-- ARGV[4] = qps_ttl  (int seconds, default 1)
-- ARGV[5] = rpm_ttl  (int seconds, default 60)
--
-- Returns 3-element array: { decision, retry_after_seconds, exhausted_axis_index }
--   decision               1=allowed, 0=denied
--   retry_after_seconds    integer ≥ 1; 0 on allowed path
--   exhausted_axis_index   0=none, 1=qps, 2=rpm, 3=tpm
--
-- Atomicity invariant (BR-X.1, BR-1.4, BR-2.4): if ANY axis is exhausted,
-- NO counter is incremented. Cross-axis short-circuit.
--
-- TPM is pre-check only (post-deduction model per BR-3.4 / Architect Q3) —
-- the script reads the TPM counter but NEVER increments it. The
-- companion tpm_deduct.lua script performs the post-deduction INCRBY.
--
-- EXPIRE ... NX (Redis 7.0+; testcontainers Redis 7.2-alpine pin per
-- tech-stack.md §2.1) preserves the fixed-window contract: TTL is set
-- only when the key is created; subsequent INCRs within the same window
-- do NOT slide the TTL (BR-1.2 / BR-2.2).

local qps_max = tonumber(ARGV[1])
local rpm_max = tonumber(ARGV[2])
local tpm_max = tonumber(ARGV[3])
local qps_ttl = tonumber(ARGV[4])
local rpm_ttl = tonumber(ARGV[5])

-- QPS axis
local qps_cur = tonumber(redis.call('GET', KEYS[1]) or 0)
if qps_cur >= qps_max then
  local ttl = redis.call('TTL', KEYS[1])
  if ttl < 0 then ttl = 1 end
  return {0, ttl, 1}
end

-- RPM axis
local rpm_cur = tonumber(redis.call('GET', KEYS[2]) or 0)
if rpm_cur >= rpm_max then
  local ttl = redis.call('TTL', KEYS[2])
  if ttl < 0 then ttl = 1 end
  return {0, ttl, 2}
end

-- TPM axis (pre-check; no INCR — post-deduction handles writes)
local tpm_cur = tonumber(redis.call('GET', KEYS[3]) or 0)
if tpm_cur >= tpm_max then
  local ttl = redis.call('TTL', KEYS[3])
  if ttl < 0 then ttl = 1 end
  return {0, ttl, 3}
end

-- All 3 axes passed. INCR QPS + RPM; set TTL with NX so the fixed window
-- is established by the first INCR of a fresh window and NOT extended on
-- subsequent INCRs within the same window. TPM is untouched here.
redis.call('INCR', KEYS[1])
redis.call('EXPIRE', KEYS[1], qps_ttl, 'NX')
redis.call('INCR', KEYS[2])
redis.call('EXPIRE', KEYS[2], rpm_ttl, 'NX')

return {1, 0, 0}
