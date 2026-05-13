# Inputs are operator-supplied via the staging env's terraform.tfvars
# (gitignored) or TF_VAR_* env vars from GitHub Actions Secrets, so
# nothing sensitive lives in manifests or workflow logs.

variable "namespace" {
  type        = string
  description = "Kubernetes namespace where the Secret + ConfigMap land. Must already exist (created by the staging env)."
}

variable "google_client_id" {
  type        = string
  description = "Google OAuth 2.0 + OIDC client ID for the He-API console origin. Provisioned in Google Cloud Console; rotation tracked in the Story 1.6 M-1 follow-up."
  sensitive   = true
}

variable "google_client_secret" {
  type        = string
  description = "Google OAuth client secret. Sourced from operator-controlled tfvars; never committed (per Story 2.2 BR-2.3 inheritance)."
  sensitive   = true
}

variable "github_client_id" {
  type        = string
  description = "GitHub OAuth App client ID for the He-API console origin."
  sensitive   = true
}

variable "github_client_secret" {
  type        = string
  description = "GitHub OAuth App client secret. Sourced from operator-controlled tfvars; never committed."
  sensitive   = true
}

variable "redirect_uri_base" {
  type        = string
  description = "Public origin used to build the OAuth callback URLs. Example: 'https://staging.api.he-api.com' or 'https://api.he-api.com'. Each provider's authorized-redirect-URI list MUST include `{base}/v1/auth/oauth/google/callback` and `{base}/v1/auth/oauth/github/callback`."
}
