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

# Dallas, Texas — AWS Dallas Local Zone (us-east-1-dfw-1), which lives in the
# us-east-1 parent region. No Graviton in the Local Zone, so this is an x86 t3
# instance (amd64 build) and IPv6 is off (Local Zones are IPv4-only). Worker +
# origin only. PREREQUISITE: the Local Zone must be opted-in on the account:
#   aws ec2 modify-availability-zone-group --region us-east-1 \
#     --group-name us-east-1-dfw-1 --opt-in-status opted-in
module "dallas" {
  source    = "./modules/node"
  providers = { aws = aws.ashburn } # Local Zone is in the us-east-1 parent region

  name                 = "flashproxy-status-dallas"
  vantage              = "aws-dallas"
  instance_type        = "t3.small" # no t4g/Graviton in the Dallas Local Zone
  go_arch              = "amd64"
  go_sha256            = "2852af0cb20a13139b3448992e69b868e50ed0f8a1e5940ee1de9e19a123b613" # go1.25.0 linux-amd64
  vpc_cidr             = "10.20.0.0/16"                                                     # distinct from ashburn's 10.10.0.0/16 (same region)
  availability_zone    = "us-east-1-dfw-1a"
  network_border_group = "us-east-1-dfw-1"
  enable_ipv6          = false
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
