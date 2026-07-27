# Remote state in S3 — so the fleet is manageable from any machine, not just the one
# box that happens to hold a terraform.tfstate file. The bucket is versioned (every
# apply keeps a recoverable prior state), SSE-S3 encrypted, public-access-blocked, and
# denies non-TLS access via bucket policy.
#
# This is a PARTIAL backend configuration: the bucket/key/region live in `backend.hcl`
# (gitignored) rather than here, because this repo is public and the state location is
# infrastructure detail. Copy backend.hcl.example -> backend.hcl, fill it in, then:
#
#   terraform init -backend-config=backend.hcl
#
# `use_lockfile` is S3-native state locking (Terraform >= 1.10) — it writes a .tflock
# object beside the state, so no DynamoDB lock table is needed.
#
# The bucket is bootstrapped OUT-OF-BAND (chicken-and-egg: it can't be managed by the
# state it stores). To recreate it, with BUCKET set to the name in backend.hcl:
#   aws s3api create-bucket --bucket "$BUCKET" --region us-east-1
#   aws s3api put-public-access-block --bucket "$BUCKET" --public-access-block-configuration \
#     BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
#   aws s3api put-bucket-versioning --bucket "$BUCKET" --versioning-configuration Status=Enabled
#   aws s3api put-bucket-encryption --bucket "$BUCKET" --server-side-encryption-configuration \
#     '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"},"BucketKeyEnabled":true}]}'
# plus a bucket policy denying requests with aws:SecureTransport = false.
terraform {
  backend "s3" {
    encrypt      = true
    use_lockfile = true
  }
}
