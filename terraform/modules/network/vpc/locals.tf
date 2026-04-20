locals {
  cluster_name           = "${var.project_name}-${var.env}"
  private_subnet_newbits = var.env == "prod" ? 6 : 8
}
