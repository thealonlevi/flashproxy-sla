terraform {
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 5.0" }
  }
}

locals {
  ipv6_pkgs = toset(["ipv6-residential", "ipv6-datacenter"])
  # The headline `connect` SLA metric targets our OWN deterministic origin (resolved
  # at boot via the __ORIGIN__/__ORIGIN6__ placeholders), not a third-party site, so
  # the number isn't polluted by Google/Cloudflare availability or per-IP blocking.
  # (Third-party reachability is still measured separately by the `scraping` scenario.)
  targets = [for pkg, u in var.proxy_urls : {
    package        = pkg
    proxy_url      = u
    connect_target = contains(local.ipv6_pkgs, pkg) ? "__ORIGIN6__" : "__ORIGIN__"
    origin_get     = "/connect"
    ip_version     = contains(local.ipv6_pkgs, pkg) ? 6 : 4
    interval_ms    = 10000
  }]
}

# Latest Ubuntu 24.04 ARM64 (Canonical)
data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"]
  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-arm64-server-*"]
  }
}

resource "aws_key_pair" "this" {
  key_name   = var.name
  public_key = var.ssh_public_key
}

# Dedicated dual-stack VPC so ipv6-egress packages can reach the origin over v6.
resource "aws_vpc" "this" {
  cidr_block                       = "10.10.0.0/16"
  assign_generated_ipv6_cidr_block = true
  enable_dns_hostnames             = true
  tags                             = { Name = var.name }
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = { Name = var.name }
}

resource "aws_subnet" "this" {
  vpc_id                          = aws_vpc.this.id
  cidr_block                      = "10.10.1.0/24"
  ipv6_cidr_block                 = cidrsubnet(aws_vpc.this.ipv6_cidr_block, 8, 1)
  map_public_ip_on_launch         = true
  assign_ipv6_address_on_creation = true
  tags                            = { Name = var.name }
}

resource "aws_route_table" "this" {
  vpc_id = aws_vpc.this.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this.id
  }
  route {
    ipv6_cidr_block = "::/0"
    gateway_id      = aws_internet_gateway.this.id
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
  ipv6_address_count     = 1 # dual-stack: origin reachable over v6 for ipv6 packages

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
    repo_url            = var.repo_url
    git_ref             = var.git_ref
    go_arch             = var.go_arch
    go_version          = var.go_version
    go_sha256           = var.go_sha256
    vantage             = var.vantage
    run_website         = var.run_website
    run_worker          = var.run_worker
    run_origin          = var.run_origin
    ch_url              = var.ch_url
    ch_worker_password  = var.ch_worker_password
    ch_website_password = var.ch_website_password
    ledger_signing_key  = var.ledger_signing_key
    ledger_pubkey       = var.ledger_pubkey
    tls_cert            = var.tls_cert
    tls_key             = var.tls_key
    targets_json        = jsonencode(local.targets)
  })

  tags = { Name = var.name }
}

resource "aws_eip" "this" {
  instance = aws_instance.this.id
  domain   = "vpc"
  tags     = { Name = var.name }
}
