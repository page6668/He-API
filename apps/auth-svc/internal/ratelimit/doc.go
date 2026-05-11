// Package ratelimit implements the five Redis-backed counters that throttle
// the auth surface (BR-4.1):
//
//   - ratelimit:signup:ip:{ip}              (BR-1.7: 5/5min)
//   - ratelimit:signin:ip:{ip}              (BR-3.8: 10/15min)
//   - ratelimit:signin:email:{email_hash}   (BR-3.8: 5/15min — soft-lock trigger)
//   - ratelimit:resend:ip:{ip}              (BR-2.4: 3/15min)
//   - ratelimit:resend:email:{email_hash}   (BR-2.4: 1/60s)
//
// All keys use email_hash = SHA-256(lower(trim(email))); plaintext emails
// never appear in Redis keys (BR-4.2, TS-CONS-005). INCR + EXPIRE is issued
// atomically (MULTI/Lua) per 2.2-UNIT-170.
//
// P2-P4 materialize the implementation as each AC needs it. P1 holds only
// this doc file.
package ratelimit
