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

# package -> connect_target (the origin in that package's PROXY region, host:port).
# Empty/unset packages fall back to the vantage-local origin placeholder.
variable "package_targets" {
  type    = map(string)
  default = {}
}

# Extra connect endpoints probed alongside each package's own origin every cycle; the
# BEST result (lowest ttfb among successes; Down only if ALL fail) is recorded, so a
# single flaky/slow destination can't masquerade as a proxy problem. These are anycast
# connectivity-check endpoints (tiny, fast, near every region, dual-stack via hostname
# so they work for v4 and v6-egress packages). Our own origin stays in the set as the
# availability floor — there is no hard external dependency.
# Provider-DIVERSE pool: the worker stratifies by `group` and probes one endpoint
# from each of GroupsPerCycle groups per cycle (plus the origin), so no single
# provider blocking proxy egress can take the package Down — a cycle always spans
# multiple independent networks. All are HTTP/80 connectivity-check endpoints with
# a fetchable body or 204 (both record ttfb; :443 CONNECT-only would zero it and
# disable the Degraded trigger). Add more groups to harden further.
variable "connect_probe_extra" {
  type = list(object({ target = string, path = string, group = string }))
  default = [
    { target = "detectportal.firefox.com:80", path = "/success.txt", group = "mozilla" },
    { target = "connectivitycheck.gstatic.com:80", path = "/generate_204", group = "google" },
    { target = "clients3.google.com:80", path = "/generate_204", group = "google" },
    { target = "www.msftconnecttest.com:80", path = "/connecttest.txt", group = "microsoft" },
    { target = "www.msftncsi.com:80", path = "/ncsi.txt", group = "microsoft" },
    { target = "captive.apple.com:80", path = "/hotspot-detect.html", group = "apple" },
    { target = "cp.cloudflare.com:80", path = "/generate_204", group = "cloudflare" },
    { target = "connectivity-check.ubuntu.com:80", path = "/", group = "ubuntu" },
    { target = "network-test.debian.org:80", path = "/nm", group = "debian" },
    { target = "nmcheck.gnome.org:80", path = "/check_network_status.txt", group = "gnome" },
  ]
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
