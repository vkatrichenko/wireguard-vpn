resource "aws_vpc_endpoint" "this" {
  vpc_id            = var.vpc_id
  service_name      = var.service_name
  vpc_endpoint_type = var.vpc_endpoint_type

  # Associate the endpoint with your private route tables.
  # This adds a route to the tables, directing S3 traffic to the endpoint.
  route_table_ids = var.private_route_table_ids

  tags = var.tags
}
