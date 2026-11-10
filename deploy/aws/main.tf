# goload on AWS: one always-on master (UI + API + gRPC coordinator +
# PostgreSQL via Docker Compose) and a launch template the master uses to
# start short-lived spot workers per run. See docs/aws.md.

data "aws_partition" "current" {}
data "aws_caller_identity" "current" {}

data "aws_vpc" "this" {
  id = var.vpc_id
}

data "aws_subnet" "master" {
  id = var.master_subnet_id
}

# Latest Amazon Linux 2023 AMI (standard, not "minimal").
data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = ["al2023-ami-2023.*-kernel-*-${var.architecture}"]
  }
  filter {
    name   = "architecture"
    values = [var.architecture]
  }
  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }
}

locals {
  name       = var.name_prefix
  partition  = data.aws_partition.current.partition
  account_id = data.aws_caller_identity.current.account_id
  arn_prefix = "arn:${local.partition}:ec2:${var.region}:${local.account_id}"
  ami_id     = data.aws_ami.al2023.id

  ssm_prefix = "/${local.name}"
  ssm_params = {
    api_token    = "${local.ssm_prefix}/api-token"
    worker_token = "${local.ssm_prefix}/worker-token"
    db_password  = "${local.ssm_prefix}/db-password"
    master_addr  = "${local.ssm_prefix}/master-grpc-addr"
  }

  grpc_port = 9090
  http_port = 8080
}

# ---------------------------------------------------------------------------
# Secrets (SSM Parameter Store, SecureString with the AWS managed key).
# Note: generated values are also stored in Terraform state; keep state
# encrypted and access-controlled.
# ---------------------------------------------------------------------------

resource "random_password" "api_token" {
  length  = 48
  special = false
}

resource "random_password" "worker_token" {
  length  = 48
  special = false
}

resource "random_password" "db_password" {
  length  = 32
  special = false
}

resource "aws_ssm_parameter" "api_token" {
  name        = local.ssm_params.api_token
  description = "goload REST API / UI bearer token"
  type        = "SecureString"
  value       = coalesce(var.api_token, random_password.api_token.result)
}

resource "aws_ssm_parameter" "worker_token" {
  name        = local.ssm_params.worker_token
  description = "goload master <-> worker shared secret"
  type        = "SecureString"
  value       = coalesce(var.worker_token, random_password.worker_token.result)
}

resource "aws_ssm_parameter" "db_password" {
  name        = local.ssm_params.db_password
  description = "goload PostgreSQL password"
  type        = "SecureString"
  value       = random_password.db_password.result
}

# Written after the master exists. The worker launch template reads it at
# boot, which avoids a dependency cycle (the master needs the template id).
resource "aws_ssm_parameter" "master_addr" {
  name        = local.ssm_params.master_addr
  description = "Private host:port of the goload master gRPC endpoint"
  type        = "String"
  value       = "${aws_instance.master.private_ip}:${local.grpc_port}"
}

# ---------------------------------------------------------------------------
# Security groups
# ---------------------------------------------------------------------------

resource "aws_security_group" "master" {
  name        = "${local.name}-master"
  description = "goload master: UI/API from admins, gRPC from workers"
  vpc_id      = data.aws_vpc.this.id
  tags        = { Name = "${local.name}-master" }
}

resource "aws_vpc_security_group_ingress_rule" "master_http" {
  for_each = toset(var.admin_cidrs)

  security_group_id = aws_security_group.master.id
  description       = "UI/API from admin network"
  ip_protocol       = "tcp"
  from_port         = local.http_port
  to_port           = local.http_port
  cidr_ipv4         = each.value
}

resource "aws_vpc_security_group_ingress_rule" "master_grpc_from_workers" {
  security_group_id            = aws_security_group.master.id
  description                  = "gRPC from goload workers only"
  ip_protocol                  = "tcp"
  from_port                    = local.grpc_port
  to_port                      = local.grpc_port
  referenced_security_group_id = aws_security_group.worker.id
}

resource "aws_vpc_security_group_egress_rule" "master_all" {
  security_group_id = aws_security_group.master.id
  description       = "AWS APIs, image registry, OS updates"
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}

# Workers accept no inbound traffic at all; they dial the master.
resource "aws_security_group" "worker" {
  name        = "${local.name}-worker"
  description = "goload workers: no inbound, egress to targets and master"
  vpc_id      = data.aws_vpc.this.id
  tags        = { Name = "${local.name}-worker" }
}

resource "aws_vpc_security_group_egress_rule" "worker_targets" {
  for_each = toset(var.worker_egress_cidrs)

  security_group_id = aws_security_group.worker.id
  description       = "Load targets, image registry, AWS APIs"
  ip_protocol       = "-1"
  cidr_ipv4         = each.value
}

resource "aws_vpc_security_group_egress_rule" "worker_to_master" {
  security_group_id            = aws_security_group.worker.id
  description                  = "gRPC to the master"
  ip_protocol                  = "tcp"
  from_port                    = local.grpc_port
  to_port                      = local.grpc_port
  referenced_security_group_id = aws_security_group.master.id
}

# ---------------------------------------------------------------------------
# IAM: worker role (minimal: read its token, pull the image, optional SSM)
# ---------------------------------------------------------------------------

data "aws_iam_policy_document" "ec2_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "worker" {
  name               = "${local.name}-worker"
  assume_role_policy = data.aws_iam_policy_document.ec2_assume.json
}

resource "aws_iam_instance_profile" "worker" {
  name = "${local.name}-worker"
  role = aws_iam_role.worker.name
}

data "aws_iam_policy_document" "worker" {
  statement {
    sid       = "ReadWorkerBootstrapParams"
    actions   = ["ssm:GetParameter"]
    resources = [aws_ssm_parameter.worker_token.arn, "arn:${local.partition}:ssm:${var.region}:${local.account_id}:parameter${local.ssm_params.master_addr}"]
  }
}

resource "aws_iam_role_policy" "worker" {
  name   = "bootstrap"
  role   = aws_iam_role.worker.id
  policy = data.aws_iam_policy_document.worker.json
}

resource "aws_iam_role_policy_attachment" "worker_ssm" {
  count      = var.worker_ssm_access ? 1 : 0
  role       = aws_iam_role.worker.name
  policy_arn = "arn:${local.partition}:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

# ---------------------------------------------------------------------------
# IAM: master role (least privilege for launching/terminating workers)
# ---------------------------------------------------------------------------

resource "aws_iam_role" "master" {
  name               = "${local.name}-master"
  assume_role_policy = data.aws_iam_policy_document.ec2_assume.json
}

resource "aws_iam_instance_profile" "master" {
  name = "${local.name}-master"
  role = aws_iam_role.master.name
}

data "aws_iam_policy_document" "master" {
  # RunInstances is authorised per resource type. The instance and its
  # volumes must carry goload:role=worker, use an allowed type and come
  # from our launch template.
  statement {
    sid       = "RunWorkerInstances"
    actions   = ["ec2:RunInstances"]
    resources = ["${local.arn_prefix}:instance/*"]

    condition {
      test     = "StringEquals"
      variable = "aws:RequestTag/goload:role"
      values   = ["worker"]
    }
    condition {
      test     = "StringEquals"
      variable = "ec2:InstanceType"
      values   = var.worker_instance_types
    }
    condition {
      test     = "ArnLike"
      variable = "ec2:LaunchTemplate"
      values   = [aws_launch_template.worker.arn]
    }
  }

  statement {
    sid       = "RunWorkerVolumes"
    actions   = ["ec2:RunInstances"]
    resources = ["${local.arn_prefix}:volume/*"]

    condition {
      test     = "StringEquals"
      variable = "aws:RequestTag/goload:role"
      values   = ["worker"]
    }
  }

  statement {
    sid     = "RunInstancesSupportingResources"
    actions = ["ec2:RunInstances"]
    resources = concat(
      [for s in var.worker_subnet_ids : "${local.arn_prefix}:subnet/${s}"],
      [
        aws_launch_template.worker.arn,
        aws_security_group.worker.arn,
        "${local.arn_prefix}:network-interface/*",
        "${local.arn_prefix}:spot-instances-request/*",
        "arn:${local.partition}:ec2:${var.region}::image/*",
      ],
    )
  }

  statement {
    sid       = "TagOnLaunchOnly"
    actions   = ["ec2:CreateTags"]
    resources = ["${local.arn_prefix}:instance/*", "${local.arn_prefix}:volume/*", "${local.arn_prefix}:spot-instances-request/*"]

    condition {
      test     = "StringEquals"
      variable = "ec2:CreateAction"
      values   = ["RunInstances"]
    }
  }

  statement {
    sid       = "TerminateOnlyWorkers"
    actions   = ["ec2:TerminateInstances"]
    resources = ["${local.arn_prefix}:instance/*"]

    condition {
      test     = "StringEquals"
      variable = "ec2:ResourceTag/goload:role"
      values   = ["worker"]
    }
  }

  statement {
    sid = "DescribeReadOnly"
    actions = [
      "ec2:DescribeInstances",
      "ec2:DescribeInstanceStatus",
      "ec2:DescribeLaunchTemplates",
      "ec2:DescribeLaunchTemplateVersions",
      "ec2:DescribeSubnets",
      "ec2:DescribeSpotPriceHistory",
    ]
    resources = ["*"]
  }

  statement {
    sid       = "PassWorkerRole"
    actions   = ["iam:PassRole"]
    resources = [aws_iam_role.worker.arn]

    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ec2.amazonaws.com"]
    }
  }

  # The first spot launch in an account creates AWSServiceRoleForEC2Spot.
  statement {
    sid       = "SpotServiceLinkedRole"
    actions   = ["iam:CreateServiceLinkedRole"]
    resources = ["arn:${local.partition}:iam::${local.account_id}:role/aws-service-role/spot.amazonaws.com/AWSServiceRoleForEC2Spot"]

    condition {
      test     = "StringEquals"
      variable = "iam:AWSServiceName"
      values   = ["spot.amazonaws.com"]
    }
  }

  statement {
    sid     = "ReadMasterSecrets"
    actions = ["ssm:GetParameter", "ssm:GetParameters"]
    resources = [
      aws_ssm_parameter.api_token.arn,
      aws_ssm_parameter.worker_token.arn,
      aws_ssm_parameter.db_password.arn,
    ]
  }
}

resource "aws_iam_role_policy" "master" {
  name   = "provision-workers"
  role   = aws_iam_role.master.id
  policy = data.aws_iam_policy_document.master.json
}

resource "aws_iam_role_policy_attachment" "master_ssm" {
  role       = aws_iam_role.master.name
  policy_arn = "arn:${local.partition}:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

# Optional private ECR access for both roles.
data "aws_iam_policy_document" "ecr_pull" {
  count = var.ecr_repository_arn == null ? 0 : 1

  statement {
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }
  statement {
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:BatchGetImage",
      "ecr:GetDownloadUrlForLayer",
    ]
    resources = [var.ecr_repository_arn]
  }
}

resource "aws_iam_role_policy" "ecr_pull" {
  for_each = {
    for k, v in { master = aws_iam_role.master.id, worker = aws_iam_role.worker.id } :
    k => v if var.ecr_repository_arn != null
  }

  name   = "ecr-pull"
  role   = each.value
  policy = data.aws_iam_policy_document.ecr_pull[0].json
}

# ---------------------------------------------------------------------------
# Worker launch template. The master overrides user data per run (see
# internal/provision); the template's own user data serves manual launches
# ("Launch instance from template") and reads master address/token from SSM.
# ---------------------------------------------------------------------------

resource "aws_launch_template" "worker" {
  name                                 = "${local.name}-worker"
  description                          = "goload load-generator worker (spot, short-lived)"
  image_id                             = local.ami_id
  instance_type                        = var.worker_instance_types[0]
  instance_initiated_shutdown_behavior = "terminate"
  update_default_version               = true
  vpc_security_group_ids               = [aws_security_group.worker.id]

  iam_instance_profile {
    arn = aws_iam_instance_profile.worker.arn
  }

  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required" # IMDSv2 only
    http_put_response_hop_limit = 2          # reachable from containers
  }

  block_device_mappings {
    device_name = "/dev/xvda"
    ebs {
      volume_size           = var.worker_volume_gb
      volume_type           = "gp3"
      encrypted             = true
      delete_on_termination = true
    }
  }

  monitoring {
    enabled = false
  }

  user_data = base64encode(templatefile("${path.module}/templates/worker-user-data.sh.tftpl", {
    region            = var.region
    image             = var.image
    ecr_registry      = var.ecr_repository_arn == null ? "" : split("/", var.image)[0]
    token_param       = local.ssm_params.worker_token
    master_addr_param = local.ssm_params.master_addr
  }))

  # Tags for manual launches. Instances launched by the master get their
  # own run-specific tags (goload:run-id=<run>).
  tag_specifications {
    resource_type = "instance"
    tags = {
      Name            = "${local.name}-worker-manual"
      "goload:role"   = "worker"
      "goload:run-id" = "manual"
    }
  }

  tag_specifications {
    resource_type = "volume"
    tags = {
      "goload:role"   = "worker"
      "goload:run-id" = "manual"
    }
  }
}

# ---------------------------------------------------------------------------
# Master instance
# ---------------------------------------------------------------------------

resource "aws_instance" "master" {
  ami                     = local.ami_id
  instance_type           = var.master_instance_type
  subnet_id               = data.aws_subnet.master.id
  vpc_security_group_ids  = [aws_security_group.master.id]
  iam_instance_profile    = aws_iam_instance_profile.master.name
  disable_api_termination = var.master_termination_protection
  monitoring              = false

  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 2 # the master container needs the instance role
  }

  root_block_device {
    volume_size           = var.master_volume_gb
    volume_type           = "gp3"
    encrypted             = true
    delete_on_termination = false # keeps PostgreSQL data if the instance is replaced
    tags                  = { Name = "${local.name}-master-root" }
  }

  # No secrets in user data: they are fetched from SSM at boot.
  user_data = templatefile("${path.module}/templates/master-user-data.sh.tftpl", {
    region                = var.region
    image                 = var.image
    ecr_registry          = var.ecr_repository_arn == null ? "" : split("/", var.image)[0]
    compose_version       = var.compose_version
    compose_arch          = var.architecture == "arm64" ? "aarch64" : "x86_64"
    api_token_param       = local.ssm_params.api_token
    worker_token_param    = local.ssm_params.worker_token
    db_password_param     = local.ssm_params.db_password
    launch_template_id    = aws_launch_template.worker.id
    worker_subnet_ids     = join(",", var.worker_subnet_ids)
    worker_instance_types = join(",", var.worker_instance_types)
    spot_only             = var.spot_only
    spot_max_price        = var.spot_max_price
    grpc_port             = local.grpc_port
    http_port             = local.http_port
  })

  # Changing user data does not re-run it on an existing instance; avoid
  # replacing the master (and its database) on every template tweak.
  lifecycle {
    ignore_changes = [ami, user_data]
  }

  tags = { Name = "${local.name}-master" }

  depends_on = [aws_iam_role_policy.master]
}

resource "aws_eip" "master" {
  count    = var.master_public_ip ? 1 : 0
  domain   = "vpc"
  instance = aws_instance.master.id
  tags     = { Name = "${local.name}-master" }
}
