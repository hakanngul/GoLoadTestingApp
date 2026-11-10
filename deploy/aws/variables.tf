# ---------------------------------------------------------------------------
# Location / network. This module never creates a VPC: bring your own.
# ---------------------------------------------------------------------------

variable "region" {
  description = "AWS region, e.g. eu-central-1."
  type        = string
}

variable "name_prefix" {
  description = "Prefix for resource names. Change it to deploy several stacks into one account."
  type        = string
  default     = "goload"
}

variable "vpc_id" {
  description = "Existing VPC to deploy into."
  type        = string
}

variable "master_subnet_id" {
  description = "Subnet for the master instance. Must be public (route to an internet gateway) when master_public_ip is true."
  type        = string
}

variable "worker_subnet_ids" {
  description = "Subnets the master may launch workers into, tried in order (ideally one per AZ). Use public subnets with auto-assign public IPv4 so load does not flow through a NAT gateway (NAT data processing is billed per GB)."
  type        = list(string)

  validation {
    condition     = length(var.worker_subnet_ids) > 0
    error_message = "At least one worker subnet is required."
  }
}

# ---------------------------------------------------------------------------
# Access
# ---------------------------------------------------------------------------

variable "admin_cidrs" {
  description = "CIDRs allowed to reach the UI/API on port 8080, e.g. [\"203.0.113.10/32\"]. Keep this narrow: port 8080 is plain HTTP (see docs/aws.md for TLS / SSM port forwarding)."
  type        = list(string)

  validation {
    condition     = !contains(var.admin_cidrs, "0.0.0.0/0")
    error_message = "Do not expose the master UI/API to the whole internet."
  }
}

variable "allowed_targets" {
  description = "Comma-separated target hosts the master lets runs hit (GOLOAD_ALLOWED_TARGETS), e.g. \"api.example.com,*.staging.example.com\". Only systems you own or have written permission to test."
  type        = string

  validation {
    condition     = trimspace(var.allowed_targets) != "" && trimspace(var.allowed_targets) != "*"
    error_message = "Set an explicit allowlist; \"*\" is for local development only."
  }
}

variable "worker_egress_cidrs" {
  description = "Where workers may send traffic. 0.0.0.0/0 is needed unless the image registry, OS repos and AWS endpoints are reachable another way. Narrowing it to the allowlisted targets adds a network-level guardrail on top of the application allowlist."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

# ---------------------------------------------------------------------------
# Master
# ---------------------------------------------------------------------------

variable "master_instance_type" {
  description = "Instance type of the always-on master (UI, API, coordinator, SQLite database)."
  type        = string
  default     = "t3.small"
}

variable "master_volume_gb" {
  description = "Root volume size of the master in GiB (holds Docker images and the SQLite database)."
  type        = number
  default     = 30
}

variable "master_public_ip" {
  description = "Attach an Elastic IP to the master. Set false when you reach it through a VPN or SSM port forwarding only."
  type        = bool
  default     = true
}

variable "master_associate_public_ip" {
  description = "Give the master an automatically assigned public IPv4 address at launch. null (default) follows the master subnet's auto-assign setting. The master needs outbound internet (image pulls, SSM, AWS APIs): set false only in a private subnet with a NAT gateway (or VPC endpoints). Independent of master_public_ip (Elastic IP). Changing it replaces the master instance."
  type        = bool
  default     = null
}

variable "master_termination_protection" {
  description = "Protect the master (and its SQLite database) against accidental termination."
  type        = bool
  default     = true
}

variable "compose_version" {
  description = "Docker Compose plugin release installed on the master."
  type        = string
  default     = "v2.29.7"
}

# ---------------------------------------------------------------------------
# Workers
# ---------------------------------------------------------------------------

variable "worker_instance_types" {
  description = "Worker instance types in fallback order. The first one is the launch template default. All must share the CPU architecture of var.architecture."
  type        = list(string)
  default     = ["c7i.large", "c6i.large", "c5.large"]
}

variable "worker_volume_gb" {
  description = "Root volume size of each worker in GiB."
  type        = number
  default     = 16
}

variable "spot_only" {
  description = "Never fall back to on-demand workers when spot capacity is unavailable."
  type        = bool
  default     = false
}

variable "spot_max_price" {
  description = "Optional spot price cap in USD/hour. Empty = on-demand price (AWS recommendation)."
  type        = string
  default     = ""
}

variable "worker_ssm_access" {
  description = "Attach AmazonSSMManagedInstanceCore to workers so you can open a shell for debugging without SSH."
  type        = bool
  default     = true
}

# ---------------------------------------------------------------------------
# Software / secrets
# ---------------------------------------------------------------------------

variable "architecture" {
  description = "CPU architecture of master and workers: x86_64 or arm64 (Graviton; then use e.g. c7g.large and a multi-arch image)."
  type        = string
  default     = "x86_64"

  validation {
    condition     = contains(["x86_64", "arm64"], var.architecture)
    error_message = "architecture must be x86_64 or arm64."
  }
}

variable "image" {
  description = "goloadtest container image for master and workers, pinned to a tag or digest, e.g. 123456789012.dkr.ecr.eu-central-1.amazonaws.com/goloadtest:v0.4.0."
  type        = string
}

variable "ecr_repository_arn" {
  description = "ARN of the ECR repository holding var.image. When set, master and workers get read access to it. Leave null for a public registry."
  type        = string
  default     = null
}

variable "api_token" {
  description = "Bearer token for the REST API/UI. Null = generate one (read it from SSM afterwards)."
  type        = string
  default     = null
  sensitive   = true
}

variable "worker_token" {
  description = "Shared secret between master and workers. Null = generate one."
  type        = string
  default     = null
  sensitive   = true
}

variable "tags" {
  description = "Extra tags for every resource (and every worker launched by the master)."
  type        = map(string)
  default     = {}
}
