variable "project_name" {
  description = "Name of the project. Used to derive resource names."
  type        = string
}

variable "env" {
  description = "The name of environment for the dashboard. Used to differentiate multiple deployments."
  type        = string
  default     = ""
}

variable "tags" {
  description = "A map of tags to assign to resources."
  type        = map(string)
}
