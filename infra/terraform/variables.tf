variable "name" {
  type    = string
  default = "labrelay-pilot"
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,30}$", var.name))
    error_message = "Use a lowercase resource prefix, 3–31 characters."
  }
}
variable "ssh_cidr" {
  type        = string
  description = "Your public IPv4 address /32. Only SSH is exposed."
  validation {
    condition     = can(cidrnetmask(var.ssh_cidr)) && endswith(var.ssh_cidr, "/32")
    error_message = "Provide a single trusted IPv4 address as /32."
  }
}
variable "ssh_public_key" { type = string }
variable "alert_email" {
  type        = string
  description = "Budget/operational alert recipient; SNS subscription requires confirmation."
}
variable "monthly_budget_usd" {
  type    = number
  default = 50
}
