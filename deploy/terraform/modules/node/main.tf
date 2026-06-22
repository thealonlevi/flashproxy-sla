terraform {
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 5.0" }
  }
}

locals {
  ipv6_pkgs = toset(["ipv6-residential", "ipv6-datacenter"])
  ami_arch  = var.go_arch == "amd64" ? "amd64" : "arm64" # match the AMI to the build arch
  # The headline `connect` probe targets one of OUR deterministic origins (never a
  # third-party site, so the number isn't polluted by external availability). The
  # specific origin per package comes from var.package_targets — the origin nearest
  # that package's PROXY — so response time isolates the proxy (see root main.tf).
  # Unmapped packages fall back to the vantage-local origin (__ORIGIN__/__ORIGIN6__,
  # resolved at boot). Third-party reachability is measured separately by `scraping`.
  targets = [for pkg, u in var.proxy_urls : {
    package   = pkg
    proxy_url = u
    # connect_target = origin nearest THIS package's proxy (proxy-region origin); if
    # not mapped, fall back to the vantage-local origin placeholder (resolved at boot).
    connect_target = lookup(var.package_targets, pkg, contains(local.ipv6_pkgs, pkg) ? "__ORIGIN6__" : "__ORIGIN__")
    origin_get     = "/connect"
    ip_version     = contains(local.ipv6_pkgs, pkg) ? 6 : 4
    interval_ms    = 10000
  }]
}

# Latest Ubuntu 24.04 (Canonical), arch matched to the build (arm64 for t4g, amd64
# for x86 instances such as Local Zones where Graviton isn't offered).
data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"]
  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-${local.ami_arch}-server-*"]
  }
}

resource "aws_key_pair" "this" {
  key_name   = var.name
  public_key = var.ssh_public_key
}

# Dedicated VPC. Dual-stack where supported so ipv6-egress packages reach the origin
# over v6; IPv6 is disabled for AWS Local Zones (which are IPv4-only).
resource "aws_vpc" "this" {
  cidr_block                       = var.vpc_cidr
  assign_generated_ipv6_cidr_block = var.enable_ipv6
  enable_dns_hostnames             = true
  tags                             = { Name = var.name }
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = { Name = var.name }
}

resource "aws_subnet" "this" {
  vpc_id                  = aws_vpc.this.id
  cidr_block              = cidrsubnet(var.vpc_cidr, 8, 1)
  map_public_ip_on_launch = true
  # Pin the AZ when given (required for a Local Zone, e.g. us-east-1-dfw-1a).
  availability_zone               = var.availability_zone != "" ? var.availability_zone : null
  ipv6_cidr_block                 = var.enable_ipv6 ? cidrsubnet(aws_vpc.this.ipv6_cidr_block, 8, 1) : null
  assign_ipv6_address_on_creation = var.enable_ipv6
  tags                            = { Name = var.name }
}

resource "aws_route_table" "this" {
  vpc_id = aws_vpc.this.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this.id
  }
  dynamic "route" {
    for_each = var.enable_ipv6 ? [1] : []
    content {
      ipv6_cidr_block = "::/0"
      gateway_id      = aws_internet_gateway.this.id
    }
  }
  tags = { Name = var.name }
}

resource "aws_route_table_association" "this" {
  subnet_id      = aws_subnet.this.id
  route_table_id = aws_route_table.this.id
}

resource "aws_security_group" "this" {
  name        = var.name
  description = "flashproxy-status node"
  vpc_id      = aws_vpc.this.id

  ingress {
    description = "SSH (admin only)"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = [var.admin_cidr]
  }

  dynamic "ingress" {
    for_each = var.run_website ? [1] : []
    content {
      description = "status page HTTP (front with Cloudflare)"
      from_port   = 80
      to_port     = 80
      protocol    = "tcp"
      cidr_blocks = ["0.0.0.0/0"]
    }
  }

  dynamic "ingress" {
    for_each = var.run_website ? [1] : []
    content {
      description = "status page HTTPS (Cloudflare Full mode connects here)"
      from_port   = 443
      to_port     = 443
      protocol    = "tcp"
      cidr_blocks = ["0.0.0.0/0"]
    }
  }

  # Origin is reached by the proxies under test (outbound from anywhere); it serves
  # deterministic payloads only.
  dynamic "ingress" {
    for_each = var.run_origin ? [1] : []
    content {
      description      = "origin"
      from_port        = 8080
      to_port          = 8080
      protocol         = "tcp"
      cidr_blocks      = ["0.0.0.0/0"]
      ipv6_cidr_blocks = ["::/0"]
    }
  }

  egress {
    from_port        = 0
    to_port          = 0
    protocol         = "-1"
    cidr_blocks      = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }
}

resource "aws_instance" "this" {
  ami                    = data.aws_ami.ubuntu.id
  instance_type          = var.instance_type
  key_name               = aws_key_pair.this.key_name
  subnet_id              = aws_subnet.this.id
  vpc_security_group_ids = [aws_security_group.this.id]
  ipv6_address_count     = var.enable_ipv6 ? 1 : 0 # dual-stack where supported (not in Local Zones)

  # Enforce IMDSv2 explicitly (don't rely on the account default) and limit the hop
  # count to 1 so a containerized/forwarded SSRF can't reach instance metadata.
  metadata_options {
    http_tokens                 = "required"
    http_endpoint               = "enabled"
    http_put_response_hop_limit = 1
  }

  # Don't recreate a running instance just because user-data text changed
  # (we reconcile in place); only a deliberate -replace rebuilds it.
  user_data_replace_on_change = false

  user_data = templatefile("${path.module}/user-data.sh.tftpl", {
    repo_url             = var.repo_url
    git_ref              = var.git_ref
    go_arch              = var.go_arch
    go_version           = var.go_version
    go_sha256            = var.go_sha256
    vantage              = var.vantage
    run_website          = var.run_website
    run_worker           = var.run_worker
    run_origin           = var.run_origin
    ch_url               = var.ch_url
    ch_worker_password   = var.ch_worker_password
    ch_website_password  = var.ch_website_password
    ledger_signing_key   = var.ledger_signing_key
    ledger_pubkey        = var.ledger_pubkey
    origin_ipv6_override = var.origin_ipv6_override
    tls_cert             = var.tls_cert
    tls_key              = var.tls_key
    targets_json         = jsonencode(local.targets)
  })

  tags = { Name = var.name }
}

resource "aws_eip" "this" {
  instance = aws_instance.this.id
  domain   = "vpc"
  # Local Zones allocate EIPs from the zone's own border group, not the region's.
  network_border_group = var.network_border_group != "" ? var.network_border_group : null
  tags                 = { Name = var.name }
}
