variable "project_name" {
  description = "Name of the project. Used to derive resource names."
  type        = string
}

variable "env" {
  description = "The name of environment for the dashboard. Used to differentiate multiple deployments."
  type        = string
  default     = ""
}

variable "target_instance_arn" {
  description = "ARN of the EC2 instance the deploy SSM document targets (the WireGuard host running the dashboard binary). When null, the deploy SSM document and its associated policy output are not produced."
  type        = string
  default     = null
}

variable "tags" {
  description = "A map of tags to assign to resources."
  type        = map(string)
}
