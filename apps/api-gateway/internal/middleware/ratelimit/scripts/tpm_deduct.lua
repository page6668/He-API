-- Story 5.3 — TPM post-deduction (BR-3.2 / Architect Q3).
--
-- KEYS[1] = ratelimit:key:{api_key_id}:tpm
-- ARGV[1] = tokens (positive int)
-- ARGV[2] = ttl_seconds (int, 60)
--
-- INCRBY + EXPIRE NX in a single atomic Lua block. The NX flag is
-- non-negotiable (Architect H-2 / M-1 — see story 5.3 Architect Round 1):
-- the go-redis Pipeline(IncrBy, Expire) form does NOT issue EXPIRE NX
-- and would slide the TTL on every call, breaking the fixed-window
-- contract Architect Q2 ratified.
--
-- Atomicity: Redis processes a Lua block in a single thread, so the
-- INCRBY and the EXPIRE NX cannot interleave with concurrent writers.

redis.call('INCRBY', KEYS[1], ARGV[1])
redis.call('EXPIRE', KEYS[1], ARGV[2], 'NX')
return 1
