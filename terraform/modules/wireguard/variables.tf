variable "preconfigured_ssh_key_id" {
  description = "A SSH public key ID to add to the VPN instance."
  type        = string
  default     = null
}

variable "project_name" {
  description = "Name of the project"
  type        = string
}

variable "cpu_architecture" {
  description = "CPU architecture for the WireGuard server. Drives the Ubuntu AMI name suffix, the AMI `architecture` filter, and the default instance type. Allowed: \"x86_64\" (amd64, t3a.micro default) or \"arm64\" (Graviton, t4g.micro default). Default \"arm64\" for better price/performance."
  type        = string
  default     = "arm64"

  validation {
    condition     = contains(["x86_64", "arm64"], var.cpu_architecture)
    error_message = "cpu_architecture must be one of: \"x86_64\", \"arm64\"."
  }
}

variable "instance_type" {
  description = "Optional explicit instance-type override. When null (default), the module uses the architecture's default (t3a.micro for x86_64, t4g.micro for arm64). A non-null value overrides the architecture-derived default."
  type        = string
  default     = null
}

variable "vpc_id" {
  description = "The VPC ID in which Terraform will launch the resources."
  type        = string
}

variable "subnet_id" {
  description = "A list of subnets. May be a single subnet, but it must be an element in a list."
  type        = string
}

variable "admin_peer" {
  description = "Single admin bootstrap peer (spec 019) — the ONLY peer Terraform seeds, for anti-lockout. `name` is rendered into /etc/wireguard-dashboard/clients.json so the dashboard can label it; `public_key` is its WireGuard public key. The module allocates the admin a fixed first-host tunnel address (the `.2` host of wg_server_net). `null` (default) seeds no peer — an empty WG peer set and empty clients.json, which is a valid deploy. Every other peer is managed in the dashboard UI, not here."
  type = object({
    name       = string
    public_key = string
  })
  default = null

  validation {
    condition     = var.admin_peer == null ? true : length(var.admin_peer.public_key) == 44
    error_message = "admin_peer.public_key must be a base64-encoded 32-byte WireGuard key (44 chars including padding)."
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
  description = "Optional explicit AMI override for the WG server. When set (non-null), it takes precedence over the cpu_architecture-derived AMI lookup. When null (default), the module resolves the latest Ubuntu 24.04 AMI for the selected cpu_architecture via the aws_ami data source."
  type        = string
  default     = null
}

variable "tags" {
  description = "A map of tags to assign to resources."
  type        = map(string)
}

variable "dashboard_release_tag" {
  description = "Required pinned GitHub Release tag (e.g. \"v1.2.3\") of the wireguard-dashboard binary to fetch at first boot. The dashboard is ALWAYS deployed alongside WireGuard (spec 019) — there is no WG-only path — so this must be a non-empty SemVer tag. User-data downloads the asset from the public release over HTTPS and verifies it against the release's SHA256SUMS before installing it (no S3, no IAM data-plane grant). This is the single source of truth for the running version — bumping it re-renders user-data, rolls a new launch-template version, and replaces the instance."
  type        = string

  validation {
    condition     = can(regex("^v[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.-]+)?$", var.dashboard_release_tag))
    error_message = "dashboard_release_tag is required and must be a SemVer tag like 'v1.2.3' (optionally with a pre-release suffix, e.g. 'v1.2.3-rc1')."
  }
}

variable "github_repo" {
  description = "GitHub repository slug (owner/name) used BOTH for the raw scripts/install.sh fetch and the dashboard release download at first boot. For install.sh it is combined with install_script_ref into the raw-content URL https://raw.githubusercontent.com/<repo>/<ref>/scripts/install.sh; for the dashboard it is combined with dashboard_release_tag into the public asset base URL https://github.com/<repo>/releases/download/<tag>/. Anonymous fetch/download requires the repo to be public."
  type        = string
  default     = "vkatrichenko/wireguard-vpn"

  validation {
    condition     = can(regex("^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$", var.github_repo))
    error_message = "github_repo must be a GitHub 'owner/name' slug."
  }
}

variable "install_script_ref" {
  description = "A commit SHA or tag pinning the exact scripts/install.sh version fetched at boot from github_repo. Must point at a ref on the PUBLIC default branch where the install.sh exists, otherwise the raw fetch 404s and boot aborts. Bumping this re-renders user-data, rolls a new launch-template version, and replaces the instance."
  type        = string
  default     = "main"
}

variable "dashboard_webhook_url_param" {
  description = "SSM parameter NAME holding the Slack-compatible alert webhook URL. Created out-of-band (like wg_server_private_key_param), not by Terraform; read at apply with `with_decryption = true` and seeded into /etc/wireguard-dashboard/alerts.env as DASHBOARD_WEBHOOK_URL. The value is a secret and is never output. Empty string (default) disables the webhook seed — alerts.env is still written with the non-secret knobs, but no DASHBOARD_WEBHOOK_URL line, so the dashboard's alerting stays dormant until the operator creates the SSM param and sets this name."
  type        = string
  default     = ""
}

variable "dashboard_slack_bot_token_param" {
  description = "SSM parameter NAME holding the Slack bot token used with chat.postMessage. Created out-of-band (like wg_server_private_key_param), not by Terraform; read at apply with `with_decryption = true` and seeded into /etc/wireguard-dashboard/alerts.env as DASHBOARD_SLACK_BOT_TOKEN. The value is a secret and is never output. Empty string (default) disables the Slack-bot transport — no DASHBOARD_SLACK_BOT_TOKEN line is written. Pair with dashboard_slack_channel."
  type        = string
  default     = ""
}

variable "dashboard_slack_channel" {
  description = "Non-secret Slack channel id or name (e.g. \"C0123456789\" or \"#alerts\") the Slack-bot transport posts to via chat.postMessage. Plain string seeded into /etc/wireguard-dashboard/alerts.env as DASHBOARD_SLACK_CHANNEL. Empty string (default) omits the line. Pair with dashboard_slack_bot_token_param."
  type        = string
  default     = ""
}

variable "dashboard_telegram_token_param" {
  description = "SSM parameter NAME holding the Telegram bot token. Created out-of-band (like wg_server_private_key_param), not by Terraform; read at apply with `with_decryption = true` and seeded into /etc/wireguard-dashboard/alerts.env as DASHBOARD_TELEGRAM_TOKEN. The value is a secret and is never output. Empty string (default) disables the Telegram transport — no DASHBOARD_TELEGRAM_TOKEN line is written. Pair with dashboard_telegram_chat_id."
  type        = string
  default     = ""
}

variable "dashboard_telegram_chat_id" {
  description = "Non-secret Telegram chat id (e.g. \"-1001234567890\") the Telegram transport sends to. Plain string seeded into /etc/wireguard-dashboard/alerts.env as DASHBOARD_TELEGRAM_CHAT_ID. Empty string (default) omits the line. Pair with dashboard_telegram_token_param."
  type        = string
  default     = ""
}

variable "dashboard_discord_webhook_url_param" {
  description = "SSM parameter NAME holding the Discord incoming-webhook URL. Created out-of-band (like wg_server_private_key_param), not by Terraform; read at apply with `with_decryption = true` and seeded into /etc/wireguard-dashboard/alerts.env as DASHBOARD_DISCORD_WEBHOOK_URL. The value is a secret and is never output. Empty string (default) disables the Discord transport — no DASHBOARD_DISCORD_WEBHOOK_URL line is written."
  type        = string
  default     = ""
}

variable "dashboard_alerts" {
  description = "Spec-007 alert thresholds seeded into /etc/wireguard-dashboard/alerts.env (mapped to DASHBOARD_HOST_LABEL / DASHBOARD_ALERT_DISK_PCT / _CPU_PCT / _CPU_SUSTAIN / _TRANSFER_BYTES). host_label empty (default) omits DASHBOARD_HOST_LABEL so the Go side falls back to os.Hostname(). cpu_sustain is a Go duration (e.g. \"5m\"); transfer_bytes is a humanized size (e.g. \"50GiB\")."
  type = object({
    host_label     = optional(string, "")
    disk_pct       = optional(number, 90)
    cpu_pct        = optional(number, 90)
    cpu_sustain    = optional(string, "5m")
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

variable "client_management_mode" {
  description = "Peer-management mode threaded from the root (spec 019). \"local\" (default) = peers managed live in the dashboard UI backed by the instance-local SQLite store only — no S3. \"cloud\" = SQLite plus a versioned S3 object used as a pure durable BACKUP: the dashboard restores from it at boot and writes it on UI edits, but Terraform never reads it and there is no drift detection. Also exported to the dashboard as CLIENT_MANAGEMENT_MODE."
  type        = string
  default     = "local"
  validation {
    condition     = contains(["local", "cloud"], var.client_management_mode)
    error_message = "client_management_mode must be either \"local\" or \"cloud\"."
  }
}
