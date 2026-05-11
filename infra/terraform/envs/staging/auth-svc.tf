# Story 2.2 — auth-svc + notification-svc Kubernetes secrets and ConfigMap.
#
# Wright Round 1 rulings:
#   Q3 ruling (APPROVED option a): JWT private key materialized by Terraform
#     (tls_private_key, RSA 4096-bit), wrapped in a K8s Secret consumed by
#     auth-svc only. Public key fans out via a ConfigMap consumed by
#     api-gateway (and any future verifying service). Vault PKI migration is
#     a documented Epic-1 follow-up tracker; staging-only path per the ruling.
#
#   Q4 ruling (APPROVED-WITH-CAVEAT option c): notification-svc lands as a
#     sibling Go service and needs SENDGRID_API_KEY at runtime — same
#     K8s-Secret-only path as Story 1.6 M-1.
#
# All resources land in the he-api-staging namespace (created in main.tf
# Section 7).

# -----------------------------------------------------------------------------
# 1. JWT signing key — RSA 4096-bit (TS-CONS-002, BR-3.5).
#    Terraform tls_private_key generates locally; the PEM-encoded private key
#    is consumed only by auth-svc, the public key fans out to verifiers.
# -----------------------------------------------------------------------------
resource "tls_private_key" "he_api_auth_jwt" {
  algorithm = "RSA"
  rsa_bits  = 4096
}

resource "kubernetes_secret" "he_api_auth_jwt_keys" {
  metadata {
    name      = "he-api-auth-jwt-keys"
    namespace = "he-api-staging"
    annotations = {
      "cloud.alibaba.com/kms-encrypted" = "true"
    }
    labels = {
      "he-api/story" = "2.2"
      "he-api/role"  = "jwt-signer"
    }
  }
  depends_on = [kubernetes_namespace.he_api_staging]
  type       = "Opaque"
  data = {
    "private_key.pem" = tls_private_key.he_api_auth_jwt.private_key_pem
    "public_key.pem"  = tls_private_key.he_api_auth_jwt.public_key_pem
  }
}

resource "kubernetes_config_map" "he_api_auth_public_keys" {
  metadata {
    name      = "he-api-auth-public-keys"
    namespace = "he-api-staging"
    labels = {
      "he-api/story" = "2.2"
      "he-api/role"  = "jwt-verifier"
    }
  }
  depends_on = [kubernetes_namespace.he_api_staging]
  data = {
    "public_key.pem" = tls_private_key.he_api_auth_jwt.public_key_pem
  }
}

# -----------------------------------------------------------------------------
# 2. notification-svc — SendGrid API key Secret (TS-CONS-010).
#    Staging consumes a SendGrid sub-account whose key is operator-supplied
#    via terraform.tfvars (gitignored, sensitive=true). Prod path mirrors
#    Story 1.6 M-1: K8s Secret only; Vault migration follow-up.
# -----------------------------------------------------------------------------
resource "kubernetes_secret" "he_api_notification_creds" {
  metadata {
    name      = "he-api-notification-creds"
    namespace = "he-api-staging"
    annotations = {
      "cloud.alibaba.com/kms-encrypted" = "true"
    }
    labels = {
      "he-api/story" = "2.2"
      "he-api/role"  = "notification-creds"
    }
  }
  depends_on = [kubernetes_namespace.he_api_staging]
  type       = "Opaque"
  data = {
    SENDGRID_API_KEY = var.sendgrid_api_key
  }
}

# -----------------------------------------------------------------------------
# Outputs — referenced by Helm values + operator handoff.
# -----------------------------------------------------------------------------
output "auth_jwt_secret_name" {
  description = "Name of the K8s Secret containing the auth-svc JWT private + public keys."
  value       = kubernetes_secret.he_api_auth_jwt_keys.metadata[0].name
}

output "auth_jwt_public_configmap_name" {
  description = "Name of the K8s ConfigMap exposing the auth-svc JWT public key to verifiers (api-gateway)."
  value       = kubernetes_config_map.he_api_auth_public_keys.metadata[0].name
}

output "notification_creds_secret_name" {
  description = "Name of the K8s Secret containing notification-svc SendGrid credentials."
  value       = kubernetes_secret.he_api_notification_creds.metadata[0].name
}
