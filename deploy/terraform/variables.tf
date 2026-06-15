variable "ssh_public_key" {
  description = "SSH public key for instance access"
  type        = string
}

variable "admin_cidr" {
  description = "Your admin IP (CIDR) allowed to SSH in"
  type        = string
}

variable "instance_type" {
  description = "ARM instance type (cheap, plenty for a prober)"
  type        = string
  default     = "t4g.small"
}

variable "image_ref" {
  description = "Container image with the worker/website/origin binaries (build & push from the repo Dockerfile)"
  type        = string
  default     = "ghcr.io/flashproxy/flashproxy-status:latest"
}

# ClickHouse endpoint (the bare-metal CH behind Cloudflare) + per-role creds.
# Passed via TF_VAR_* env, never committed.
variable "ch_url" {
  type    = string
  default = "https://ch.flashproxy.com"
}
variable "ch_worker_password" {
  type      = string
  sensitive = true
}
variable "ch_website_password" {
  type      = string
  sensitive = true
}

# The 5 proxy plans the workers probe (user:pass@host:port). Sensitive.
variable "proxy_urls" {
  description = "package -> http://user:pass@host:port"
  type        = map(string)
  sensitive   = true
  # e.g. { isp = "http://USER:PASS@isp.example.com:30", ... }
}
