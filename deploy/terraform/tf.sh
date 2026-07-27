#!/usr/bin/env bash
# Run terraform with secrets streamed from S3 — never written to disk.
#
#   ./tf.sh plan
#   ./tf.sh apply
#   ./tf.sh state list
#   ./tf.sh ssh ashburn          # break-glass SSH using the key from S3
#
# WHY NOT a Terraform data source? `data "aws_secretsmanager_secret_version"` (and
# `aws_ssm_parameter`) persist the fetched value into the state file in PLAINTEXT.
# The state currently contains NO secrets — the AWS provider stores user_data as a
# SHA1 hash, not the rendered script — and that property is worth keeping. Passing
# secrets as -var-file keeps them variables: they land in user_data, which state only
# hashes. So they never hit the bucket in readable form and never hit local disk.
#
# Everything needed lives in S3; the only thing you must bring is an AWS identity
# (instance profile, SSO, or access key) that can read the bucket in backend.hcl.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

if [[ ! -f backend.hcl ]]; then
  echo "tf.sh: backend.hcl missing — copy backend.hcl.example and fill it in." >&2
  exit 1
fi

BUCKET="$(awk -F'"' '/^[[:space:]]*bucket/ {print $2}' backend.hcl)"
PREFIX="$(awk -F'"' '/^[[:space:]]*key/ {print $2}' backend.hcl | xargs dirname)"
SECRETS="s3://${BUCKET}/${PREFIX}/secrets"

if [[ -z "$BUCKET" ]]; then
  echo "tf.sh: could not parse bucket from backend.hcl" >&2
  exit 1
fi

# Break-glass SSH: pull the private key into a 0600 file in a private tmpdir,
# shred it on exit. Instance IPs come from terraform outputs.
if [[ "${1:-}" == "ssh" ]]; then
  node="${2:?usage: ./tf.sh ssh <ashburn|frankfurt|dallas>}"
  d="$(mktemp -d)"
  chmod 700 "$d"
  trap 'shred -u "$d/key" 2>/dev/null || rm -f "$d/key"; rmdir "$d" 2>/dev/null || true' EXIT
  aws s3 cp "${SECRETS}/flashproxy-sla" "$d/key" --quiet
  chmod 600 "$d/key"
  ip="$(terraform output -raw "${node}_eip")"
  exec ssh -i "$d/key" -o IdentitiesOnly=yes "ubuntu@${ip}"
fi

# terraform init needs the backend config but not the vars.
if [[ "${1:-}" == "init" ]]; then
  shift
  exec terraform init -backend-config=backend.hcl "$@"
fi

# Subcommands that take no -var-file.
case "${1:-}" in
  state|output|version|providers|fmt|force-unlock|workspace|show)
    exec terraform "$@"
    ;;
esac

# Process substitution: terraform reads /dev/fd/N, so the plaintext exists only in
# a pipe owned by this shell — never as a file, never in shell history, never in ps.
exec terraform "$@" -var-file=<(aws s3 cp "${SECRETS}/terraform.tfvars" - --quiet)
