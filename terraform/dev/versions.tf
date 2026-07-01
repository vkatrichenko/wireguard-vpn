terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "= 6.41.0"
    }
    restapi = {
      source  = "Mastercard/restapi"
      version = "= 3.0.0"
    }
  }
  required_version = "= 1.14.8"
}
