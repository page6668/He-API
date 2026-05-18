# Aliyun OSS bucket for GDPR data-export ZIP packages — Story 2.6 AC4.
#
# BR-4.5 contract:
#   - cn-shanghai region (compliance §9.1 境内; TS-CONS-003).
#   - ACL `private` (no public read).
#   - lifecycle rule "delete objects older than 25 hours" — safety net
#     against orphan ZIPs from failed jobs + defense-in-depth alongside
#     the future BR-4.7 cron (deferred Story).
#   - SSE-KMS server-side encryption.
#   - bucket policy restricts access to analytics-svc + notification-svc
#     RAM roles only (no public read; signed URLs are the user-facing
#     access channel).

resource "alicloud_oss_bucket" "this" {
  bucket = var.bucket_name
  acl    = "private"

  # SSE-KMS server-side encryption (parity with state-bucket bootstrap).
  server_side_encryption_rule {
    sse_algorithm     = "KMS"
    kms_master_key_id = var.kms_key_id
  }

  versioning {
    status = "Enabled"
  }

  # BR-4.5 safety net — Aliyun OSS lifecycle expects days; 25 hours is
  # ceil'd to 2 days here intentionally for the prefix scope. The ZIPs
  # are bounded above by the signed-URL window (24h) + 1h skew per AC4
  # step-7 (`signed_url_expires_at = completed_at + 24h`); the lifecycle
  # rule is the storage safety floor, NOT a tight 25h timer (the future
  # BR-4.7 cron flips status='completed' → 'expired' and analytics-svc
  # may also DeleteObject directly on the cron schedule). 2 days is the
  # smallest Aliyun-supported unit for prefix-scoped expiration.
  lifecycle_rule {
    id      = "expire-gdpr-export-zips"
    enabled = true
    prefix  = "gdpr-exports/"

    expiration {
      days = 2
    }

    abort_multipart_upload {
      days = 1
    }
  }
}

# Bucket policy — analytics-svc PutObject + notification-svc GetObject.
# Per AC4 BR-4.5 + AC5 BR-5.2 (notification-svc treats signed URLs as
# opaque credentials; it does not need bucket-level read either, but
# the Aliyun SDK SignURL pre-signing path under a RAM-bound caller is
# the canonical pattern).
resource "alicloud_oss_bucket_policy" "this" {
  bucket = alicloud_oss_bucket.this.bucket

  policy = jsonencode({
    Version = "1"
    Statement = [
      {
        Effect    = "Allow"
        Principal = { RAM = [var.analytics_svc_ram_role_arn] }
        Action = [
          "oss:PutObject",
          "oss:GetObject",
          "oss:DeleteObject",
          "oss:ListObjectsV2",
        ]
        Resource = [
          "acs:oss:*:*:${alicloud_oss_bucket.this.bucket}",
          "acs:oss:*:*:${alicloud_oss_bucket.this.bucket}/gdpr-exports/*",
        ]
      },
      {
        Effect    = "Allow"
        Principal = { RAM = [var.notification_svc_ram_role_arn] }
        Action = [
          # GetObject so the SignURL flow can validate the object exists
          # before issuing the signed URL.
          "oss:GetObject",
        ]
        Resource = [
          "acs:oss:*:*:${alicloud_oss_bucket.this.bucket}/gdpr-exports/*",
        ]
      },
    ]
  })
}
