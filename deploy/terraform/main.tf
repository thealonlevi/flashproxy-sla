# Two regions: Ashburn (us-east-1) runs worker + origin + website (the EU-facing
# public page). Frankfurt (eu-central-1) runs worker + origin (EU vantage for isp_eu).
# Both workers write to the bare-metal ClickHouse via https://ch.flashproxy.com.

module "ashburn" {
  source    = "./modules/node"
  providers = { aws = aws.ashburn }

  name           = "flashproxy-status-ashburn"
  vantage        = "aws-ashburn"
  instance_type  = var.instance_type
  image_ref      = var.image_ref
  ssh_public_key = var.ssh_public_key
  admin_cidr     = var.admin_cidr

  run_website = true # serves status.flashproxy.com
  run_worker  = true
  run_origin  = true

  ch_url              = var.ch_url
  ch_worker_password  = var.ch_worker_password
  ch_website_password = var.ch_website_password
  proxy_urls          = var.proxy_urls
}

module "frankfurt" {
  source    = "./modules/node"
  providers = { aws = aws.frankfurt }

  name           = "flashproxy-status-frankfurt"
  vantage        = "aws-frankfurt"
  instance_type  = var.instance_type
  image_ref      = var.image_ref
  ssh_public_key = var.ssh_public_key
  admin_cidr     = var.admin_cidr

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
output "frankfurt_eip" {
  value = module.frankfurt.public_ip
}
