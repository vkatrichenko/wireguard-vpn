variable "preconfigured_ssh_key_id" {
  description = "A SSH public key ID to add to the VPN instance."
  type        = string
  default     = null
}

variable "project_name" {
  description = "Name of the project"
  type        = string
}

variable "instance_type" {
  description = "The machine type to launch, some machines may offer higher throughput for higher use cases."
  type        = string
  default     = "t3a.micro"
}

variable "vpc_id" {
  description = "The VPC ID in which Terraform will launch the resources."
  type        = string
}

variable "subnet_id" {
  description = "A list of subnets. May be a single subnet, but it must be an element in a list."
  type        = string
}

variable "clients_config" {
  description = "List of maps of client IPs and public keys. See Usage in README for details."
  type        = any
}

variable "wg_server_net" {
  description = "IP range for vpn server - make sure your Client ips are in this range but not the specific ip i.e. not .1"
  type        = string
}

variable "wg_server_port" {
  description = "Port for the vpn server."
  type        = number
  default     = 51820
}

variable "wg_persistent_keepalive" {
  description = "Persistent Keepalive - useful for helping connection stability over NATs."
  type        = number
  default     = 25
}

variable "use_eip" {
  description = "Whether to enable Elastic IP switching code in user-data on wg server startup. If true, eip_id must also be set to the ID of the Elastic IP."
  type        = bool
  default     = true
}

variable "additional_security_group_ids" {
  description = "Additional security groups if provided, default empty."
  type        = list(string)
  default     = [""]
}

variable "env" {
  description = "The name of environment for WireGuard. Used to differentiate multiple deployments."
  type        = string
}

variable "wg_server_private_key_param" {
  description = "The SSM parameter containing the WG server private key."
  type        = string
}

variable "ami_id" {
  description = "The AWS AMI to use for the WG server, defaults to the latest Ubuntu 16.04 AMI if not specified."
  type        = string
  default     = null
}

variable "tags" {
  description = "A map of tags to assign to resources."
  type        = map(string)
}

variable "dashboard_artifact_bucket_arn" {
  description = "ARN of the S3 bucket that holds the web-dashboard binary artifacts. When non-null, the EC2 instance role gains scoped read access to objects under `latest/*` and `main-*/*`. When null (default), no dashboard read statements are added to the instance policy. Wired in by `dev/main.tf` once the dashboard module is composed in."
  type        = string
  default     = null

  validation {
    condition     = var.dashboard_artifact_bucket_arn == null || can(regex("^arn:aws:s3:::", var.dashboard_artifact_bucket_arn))
    error_message = "dashboard_artifact_bucket_arn must be null or a valid S3 bucket ARN starting with 'arn:aws:s3:::'."
  }
}

variable "dashboard_artifact_bucket_name" {
  description = "Name of the S3 bucket that hosts the web-dashboard binary artifacts. When non-null, first-boot user-data downloads `s3://<bucket>/latest/wireguard-dashboard`, installs it under `/opt/wireguard-dashboard/bin/`, and starts a `wireguard-dashboard.service` systemd unit bound to the WireGuard tunnel IP. When null (default), the dashboard is not provisioned and user-data behaves as before. Paired with `dashboard_artifact_bucket_arn` (which gates the IAM read permissions); both are wired in by `dev/main.tf` once the dashboard module is composed in."
  type        = string
  default     = null

  validation {
    condition     = var.dashboard_artifact_bucket_name == null || can(regex("^[a-z0-9.\\-]{3,63}$", var.dashboard_artifact_bucket_name))
    error_message = "dashboard_artifact_bucket_name must be null or a valid S3 bucket name (3-63 chars, lowercase letters, digits, hyphens, dots)."
  }
}
