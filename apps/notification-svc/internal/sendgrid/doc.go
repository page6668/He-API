// Package sendgrid wraps the SendGrid HTTPS API.
//
// Credentials: SENDGRID_API_KEY from K8s Secret he-api-notification-creds
// (Terraform-injected; TS-CONS-010). Staging consumes a SendGrid sub-account
// distinct from prod.
//
// P2 (T1, AC1) materializes the implementation. P1 holds only this doc file.
package sendgrid
