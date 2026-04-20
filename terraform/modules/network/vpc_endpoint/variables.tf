variable "vpc_id" {
  description = "ID of the VPC"
  type        = string
}

variable "service_name" {
  description = "The name of the AWS service to create the VPC endpoint for"
  type        = string
}

variable "vpc_endpoint_type" {
  description = "Type of VPC endpoint (Gateway or Interface)"
  type        = string
  default     = "Gateway"
}

variable "private_route_table_ids" {
  description = "List of private route table IDs for the endpoint"
  type        = list(string)
}

variable "tags" {
  description = "Map of tags to apply to resources"
  type        = map(string)
}
