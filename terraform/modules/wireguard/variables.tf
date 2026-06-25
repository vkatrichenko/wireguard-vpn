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
  description = "List of WireGuard peer (client) definitions. Each entry's `name` is rendered into /etc/wireguard-dashboard/clients.json by user-data so the dashboard can label peers; `address` is the CIDR the peer is allowed inside the WG subnet (e.g. \"172.16.15.6/32\"); `public_key` is the peer's WireGuard public key."
  type = list(object({
    name       = string
    address    = string
    public_key = string
  }))

  validation {
    condition     = alltrue([for c in var.clients_config : can(regex("^[0-9.]+/32$", c.address))])
    error_message = "Every clients_config entry's `address` must be an IPv4 /32 CIDR (e.g. \"172.16.15.6/32\")."
  }

  validation {
    condition     = alltrue([for c in var.clients_config : length(c.public_key) == 44])
    error_message = "Every clients_config entry's `public_key` must be a base64-encoded 32-byte WireGuard key (44 chars including padding)."
  }
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

variable "dashboard_release_tag" {
  description = "Pinned GitHub Release tag (e.g. \"v1.2.3\") of the wireguard-dashboard binary to fetch at first boot. When non-empty, user-data downloads the asset from the public release over HTTPS and verifies it against the release's SHA256SUMS before installing it (no S3, no IAM data-plane grant). Empty string (default) disables dashboard provisioning. This is the single source of truth for the running version — bumping it re-renders user-data, rolls a new launch-template version, and replaces the instance."
  type        = string
  default     = ""

  validation {
    condition     = var.dashboard_release_tag == "" || can(regex("^v[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.-]+)?$", var.dashboard_release_tag))
    error_message = "dashboard_release_tag must be empty or a SemVer tag like 'v1.2.3' (optionally with a pre-release suffix, e.g. 'v1.2.3-rc1')."
  }
}

variable "dashboard_release_repo" {
  description = "GitHub repository slug (owner/name) the dashboard release is fetched from. Combined with dashboard_release_tag into the public asset base URL https://github.com/<repo>/releases/download/<tag>/. Anonymous download requires the repo to be public."
  type        = string
  default     = "vkatrichenko/wireguard-vpn"

  validation {
    condition     = can(regex("^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$", var.dashboard_release_repo))
    error_message = "dashboard_release_repo must be a GitHub 'owner/name' slug."
  }
}

variable "dashboard_webhook_url_param" {
  description = "SSM parameter NAME holding the Slack-compatible alert webhook URL. Created out-of-band (like wg_server_private_key_param), not by Terraform; read at apply with `with_decryption = true` and seeded into /etc/wireguard-dashboard/alerts.env as DASHBOARD_WEBHOOK_URL. The value is a secret and is never output. Empty string (default) disables the webhook seed — alerts.env is still written with the non-secret knobs, but no DASHBOARD_WEBHOOK_URL line, so the dashboard's alerting stays dormant until the operator creates the SSM param and sets this name."
  type        = string
  default     = ""
}

variable "dashboard_alerts" {
  description = "Spec-007 alert thresholds seeded into /etc/wireguard-dashboard/alerts.env (mapped to DASHBOARD_HOST_LABEL / DASHBOARD_ALERT_DISK_PCT / _CPU_PCT / _CPU_SUSTAIN / _PEER_STALE / _TRANSFER_BYTES). host_label empty (default) omits DASHBOARD_HOST_LABEL so the Go side falls back to os.Hostname(). cpu_sustain/peer_stale are Go durations (e.g. \"5m\"); transfer_bytes is a humanized size (e.g. \"50GiB\")."
  type = object({
    host_label     = optional(string, "")
    disk_pct       = optional(number, 90)
    cpu_pct        = optional(number, 90)
    cpu_sustain    = optional(string, "5m")
    peer_stale     = optional(string, "10m")
    transfer_bytes = optional(string, "50GiB")
  })
  default = {}

  validation {
    condition     = var.dashboard_alerts.disk_pct >= 1 && var.dashboard_alerts.disk_pct <= 100
    error_message = "dashboard_alerts.disk_pct must be between 1 and 100."
  }

  validation {
    condition     = var.dashboard_alerts.cpu_pct >= 1 && var.dashboard_alerts.cpu_pct <= 100
    error_message = "dashboard_alerts.cpu_pct must be between 1 and 100."
  }
}
