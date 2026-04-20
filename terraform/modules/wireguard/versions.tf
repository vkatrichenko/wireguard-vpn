terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "= 6.41.0"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "= 4.0.3"
    }
    null = {
      source  = "hashicorp/null"
      version = "= 3.2.4"
    }
  }
  required_version = "= 1.14.8"
}
