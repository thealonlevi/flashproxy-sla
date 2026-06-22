# Two regions: Ashburn (us-east-1) runs worker + origin + website (the EU-facing
# public page). Frankfurt (eu-central-1) runs worker + origin (EU vantage for isp_eu).
# Both workers write to the bare-metal ClickHouse via https://ch.flashproxy.com.

module "ashburn" {
  source    = "./modules/node"
  providers = { aws = aws.ashburn }

  name           = "flashproxy-status-ashburn"
  vantage        = "aws-ashburn"
  instance_type  = var.instance_type
  repo_url       = var.repo_url
  git_ref        = var.git_ref
  ssh_public_key = var.ssh_public_key
  admin_cidr     = var.admin_cidr

  run_website = true # serves status.flashproxy.com
  run_worker  = true
  run_origin  = true

  ch_url              = var.ch_url
  ch_worker_password  = var.ch_worker_password
  ch_website_password = var.ch_website_password
  ledger_signing_key  = var.ledger_signing_key # monitor node signs checkpoints
  ledger_pubkey       = var.ledger_pubkey      # website publishes the public key
  tls_cert            = var.tls_cert
  tls_key             = var.tls_key
  proxy_urls          = var.proxy_urls
}

module "frankfurt" {
  source    = "./modules/node"
  providers = { aws = aws.frankfurt }

  name           = "flashproxy-status-frankfurt"
  vantage        = "aws-frankfurt"
  instance_type  = var.instance_type
  repo_url       = var.repo_url
  git_ref        = var.git_ref
  ssh_public_key = var.ssh_public_key
  admin_cidr     = var.admin_cidr

  run_website = false
  run_worker  = true
  run_origin  = true

  ch_url             = var.ch_url
  ch_worker_password = var.ch_worker_password
  proxy_urls         = var.proxy_urls
}

# Dallas, Texas — AWS Dallas Local Zone (us-east-1-dfw-2), which lives in the
# us-east-1 parent region. The zone has no t-family/small sizes but does offer
# Graviton, so this runs the smallest available ARM instance (m6g.medium, arm64 —
# same build arch as the other nodes). IPv6 is off (Local Zones are IPv4-only).
# Worker + origin only. PREREQUISITE: the Local Zone must be opted-in on the account:
#   aws ec2 modify-availability-zone-group --region us-east-1 \
#     --group-name us-east-1-dfw-2 --opt-in-status opted-in
module "dallas" {
  source    = "./modules/node"
  providers = { aws = aws.ashburn } # Local Zone is in the us-east-1 parent region

  name                 = "flashproxy-status-dallas"
  vantage              = "aws-dallas"
  instance_type        = "m6g.medium" # smallest size offered in dfw-2a (Graviton/arm64)
  go_arch              = "arm64"
  go_sha256            = "05de75d6994a2783699815ee553bd5a9327d8b79991de36e38b66862782f54ae" # go1.25.0 linux-arm64
  vpc_cidr             = "10.20.0.0/16"                                                     # distinct from ashburn's 10.10.0.0/16 (same region)
  availability_zone    = "us-east-1-dfw-2a"
  network_border_group = "us-east-1-dfw-2"
  enable_ipv6          = false
  # v4-only Local Zone: ipv6-egress packages need a v6-reachable target, so borrow
  # Ashburn's dual-stack origin over IPv6 (the proxy is still reached over IPv4).
  origin_ipv6_override = "[${module.ashburn.public_ipv6}]:8080"
  repo_url             = var.repo_url
  git_ref              = var.git_ref
  ssh_public_key       = var.ssh_public_key
  admin_cidr           = var.admin_cidr

  run_website = false
  run_worker  = true
  run_origin  = true

  ch_url             = var.ch_url
  ch_worker_password = var.ch_worker_password
  proxy_urls         = var.proxy_urls
}

output "ashburn_eip" {
  description = "Point status.flashproxy.com A-record here (proxied)"
  value       = module.ashburn.public_ip
}

output "dallas_eip" {
  value = module.dallas.public_ip
}
output "frankfurt_eip" {
  value = module.frankfurt.public_ip
}
