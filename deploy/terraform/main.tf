# Two regions: Ashburn (us-east-1) runs worker + origin + website (the EU-facing
# public page). Frankfurt (eu-central-1) runs worker + origin (EU vantage for isp_eu).
# Both workers write to the bare-metal ClickHouse via https://ch.flashproxy.com.

locals {
  # Each package's connect_target is the origin in that package's PROXY region, NOT
  # the vantage's own origin — so response time isolates the proxy (vantage->proxy +
  # proxy->co-located origin ~= vantage->proxy). Measured proxy homes: datacenter /
  # ipv6-datacenter ~4ms from Ashburn (us-east); isp ~now in Dallas; isp_eu ~10ms
  # from Frankfurt (EU). EIPs are stable; the Ashburn v6 is the instance address —
  # update it if the Ashburn node is replaced (or move these to stable DNS A/AAAA).
  ashburn_v4   = "52.204.201.83:8080"
  ashburn_v6   = "[2600:1f18:3322:9d01:d95c:17fe:e48a:3ea1]:8080"
  frankfurt_v4 = "18.193.186.1:8080"
  dallas_v4    = "18.88.33.127:8080" # Dallas Local Zone origin (v4 only — LZ has no IPv6)
  package_targets = {
    "datacenter"       = local.ashburn_v4
    "isp"              = local.dallas_v4 # ISP proxy moved to Dallas → co-locate the origin
    "isp_eu"           = local.frankfurt_v4
    "ipv6-datacenter"  = local.ashburn_v6
    # ipv6-residential proxy is moving to Dallas, but the Dallas LZ has NO IPv6 origin,
    # so its v6-egress still targets the nearest v6 origin we run (Ashburn ~30ms). That
    # ~30ms is geographic distance to the test origin, not proxy overhead — unavoidable
    # until a v6-reachable origin exists near Dallas (the LZ can't provide one).
    "ipv6-residential" = local.ashburn_v6
  }
}

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
  package_targets     = local.package_targets
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
  package_targets    = local.package_targets
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
  package_targets    = local.package_targets
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
