// Package notification wraps the outbound gRPC client to notification-svc
// (Wright Round 1 Q4 ruling option c — synchronous SendEmail).
//
// Surfaces:
//   - SendVerificationEmail(to, locale, token, link) → calls
//     NotificationService.SendEmail(template=EMAIL_VERIFICATION, variables={token,
//     verification_link})
//
// Behavior on notification-svc failure (BR-1.9): auth-svc returns 500
// `500_email_send_failed`; the users row persists with email_verified_at=NULL
// so the user can retry via ResendVerification. Async Kafka migration is an
// Epic-9 follow-up (TS-CONS-009 caveat).
//
// P2 (T1, AC1) materializes the implementation. P1 holds only this doc file.
package notification
