variable "vpc_name" {
  description = "Name of the VPC"
  type        = string
}

variable "env" {
  description = "Deployment environment name"
  type        = string
  default     = ""
}

variable "vpc_cidr" {
  description = "CIDR block for the VPC"
  type        = string
}

variable "tags" {
  description = "Map of tags to apply to resources"
  type        = map(string)
}

variable "project_name" {
  description = "Name of the project"
  type        = string
}

variable "ports" {
  description = "List of ports to be used for security group ingress rules"
  type        = list(string)
}

variable "create_database_subnets" {
  description = "Whether to create database subnets"
  type        = bool
  default     = false
}

variable "create_intra_subnets" {
  description = "Whether to create intra subnets"
  type        = bool
  default     = false
}
