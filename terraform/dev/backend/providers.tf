terraform {
  backend "s3" {
    bucket       = "wireguard-vpn-test-tf-states"
    key          = "backend/tf.tfstate"
    region       = "us-east-1"
    encrypt      = true
    use_lockfile = true
  }
}

provider "aws" {
  region = local.aws_region
  default_tags {
    tags = local.default_tags
  }
}
