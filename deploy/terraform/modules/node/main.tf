terraform {
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 5.0" }
  }
}

locals {
  ipv6_pkgs = toset(["ipv6", "ipv6-datacenter"])
  targets = [for pkg, u in var.proxy_urls : {
    package        = pkg
    proxy_url      = u
    connect_target = "www.google.com:443"
    origin_get     = ""
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

resource "aws_security_group" "this" {
  name        = var.name
  description = "flashproxy-status node"

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
      description = "status page (front with Cloudflare)"
      from_port   = 80
      to_port     = 80
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
  vpc_security_group_ids = [aws_security_group.this.id]

  user_data = templatefile("${path.module}/user-data.sh.tftpl", {
    repo_url            = var.repo_url
    git_ref             = var.git_ref
    go_arch             = var.go_arch
    vantage             = var.vantage
    run_website         = var.run_website
    run_worker          = var.run_worker
    run_origin          = var.run_origin
    ch_url              = var.ch_url
    ch_worker_password  = var.ch_worker_password
    ch_website_password = var.ch_website_password
    targets_json        = jsonencode(local.targets)
  })

  tags = { Name = var.name }
}

resource "aws_eip" "this" {
  instance = aws_instance.this.id
  domain   = "vpc"
  tags     = { Name = var.name }
}
