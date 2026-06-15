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
  default = "arm64" # t4g instances
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
