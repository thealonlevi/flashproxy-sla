variable "name" { type = string }
variable "vantage" { type = string }
variable "instance_type" { type = string }
variable "image_ref" { type = string }
variable "ssh_public_key" { type = string }
variable "admin_cidr" { type = string }

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
variable "proxy_urls" {
  type      = map(string)
  sensitive = true
}
