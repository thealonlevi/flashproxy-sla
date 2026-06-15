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

variable "repo_url" {
  description = "Public git repo the instances build from (stdlib-only, no module downloads)"
  type        = string
  default     = "https://github.com/thealonlevi/flashproxy-sla.git"
}

variable "git_ref" {
  description = "Branch/tag/sha to build"
  type        = string
  default     = "main"
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

# Optional origin TLS for the website. Leave empty to use a self-signed cert
# (Cloudflare SSL mode "Full"). For "Full (strict)", paste a Cloudflare Origin
# Certificate + key here (via TF_VAR_tls_cert / TF_VAR_tls_key or tfvars).
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

# Integrity-ledger Ed25519 keys (generate with `go run ./cmd/keygen`). The private
# seed goes to the monitor node only; the public key is published by the website.
# Pass via TF_VAR_* / tfvars, never committed.
variable "ledger_signing_key" {
  type      = string
  sensitive = true
  default   = ""
}
variable "ledger_pubkey" {
  type    = string
  default = ""
}

# The 5 proxy plans the workers probe (user:pass@host:port). Sensitive.
variable "proxy_urls" {
  description = "package -> http://user:pass@host:port"
  type        = map(string)
  sensitive   = true
  # e.g. { isp = "http://USER:PASS@isp.example.com:30", ... }
}
