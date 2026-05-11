// Package audit publishes auth domain events to the Kafka topic audit.event
// (TS-CONS-015, BR-4.5).
//
// Event types (8 introduced by Story 2.2):
//   auth.signup
//   auth.signup_duplicate_attempt
//   auth.verify_email
//   auth.verify_email_brute_force
//   auth.signin_success
//   auth.signin_failure
//   auth.account_locked
//   auth.email_send_failed
//
// Publish failure does NOT block the request path (BR-4.5; TS-CONS-009 async
// audit). On Kafka outage: warn-log + Sentry; handler still returns the
// business response.
//
// Plaintext emails and passwords are NEVER included in payloads — email_hash =
// SHA-256(lower(trim(email))) only; password fields not in the struct at all.
//
// P2-P4 materialize the implementation. P1 holds only this doc file.
package audit
