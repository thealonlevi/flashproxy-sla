variable "name" { type = string }
variable "vantage" { type = string }
variable "instance_type" { type = string }
variable "ssh_public_key" { type = string }
variable "admin_cidr" { type = string }

variable "repo_url" { type = string }
variable "git_ref" {
  type    = string
  default = "main"
}
variable "go_arch" {
  type    = string
  default = "arm64" # t4g instances; "amd64" for x86 (e.g. Local Zones without Graviton)
}

variable "vpc_cidr" {
  type    = string
  default = "10.10.0.0/16"
}

# Pin a specific AZ — REQUIRED for an AWS Local Zone (e.g. "us-east-1-dfw-1a").
# Empty lets AWS pick a default AZ in the region.
variable "availability_zone" {
  type    = string
  default = ""
}

# A Local Zone allocates EIPs from its own network border group (e.g.
# "us-east-1-dfw-1"). Empty uses the region default.
variable "network_border_group" {
  type    = string
  default = ""
}

# Local Zones are IPv4-only; set false there to skip all IPv6 provisioning.
variable "enable_ipv6" {
  type    = bool
  default = true
}

# For a node with no local IPv6 (Local Zone), the v6-reachable origin to use for
# ipv6-egress packages, e.g. "[<dual-stack-node-v6>]:8080". Empty => use the local
# origin (the normal dual-stack case). The proxy is reached over IPv4 either way;
# this is only the target the proxy egresses to over IPv6.
variable "origin_ipv6_override" {
  type    = string
  default = ""
}
variable "go_version" {
  type    = string
  default = "1.25.0"
}
# SHA-256 of go<version>.linux-<arch>.tar.gz from https://go.dev/dl/?mode=json
# (default is go1.25.0 linux-arm64). Keep in sync with go_version/go_arch.
variable "go_sha256" {
  type    = string
  default = "05de75d6994a2783699815ee553bd5a9327d8b79991de36e38b66862782f54ae"
}

# Integrity-ledger signing. Only the monitor node (run_website) needs the private
# key; it signs checkpoints for ALL streams. The public key is published by the
# website so anyone can verify.
variable "ledger_signing_key" {
  type      = string
  sensitive = true
  default   = ""
}
variable "ledger_pubkey" {
  type    = string
  default = ""
}

variable "run_website" {
  type    = bool
  default = false
}
variable "run_worker" {
  type    = bool
  default = true
}
variable "run_origin" {
  type    = bool
  default = true
}

variable "ch_url" { type = string }
variable "ch_worker_password" {
  type      = string
  sensitive = true
}
variable "ch_website_password" {
  type      = string
  sensitive = true
  default   = ""
}
# Optional origin TLS for the website (:443). If empty, user-data generates a
# self-signed cert (works with Cloudflare SSL mode "Full"; for "Full (strict)"
# supply a Cloudflare Origin Certificate here).
variable "tls_cert" {
  type      = string
  sensitive = true
  default   = ""
}
variable "tls_key" {
  type      = string
  sensitive = true
  default   = ""
}
variable "proxy_urls" {
  type      = map(string)
  sensitive = true
}
