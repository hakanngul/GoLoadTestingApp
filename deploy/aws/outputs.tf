output "master_url" {
  description = "UI/API URL (reachable from admin_cidrs). Empty when master_public_ip is false; use SSM port forwarding then."
  value       = var.master_public_ip ? "http://${aws_eip.master[0].public_ip}:${local.http_port}" : ""
}

output "master_private_grpc_addr" {
  description = "host:port workers dial."
  value       = aws_ssm_parameter.master_addr.value
}

output "master_instance_id" {
  description = "Master instance id (for SSM Session Manager / port forwarding)."
  value       = aws_instance.master.id
}

output "launch_template_id" {
  description = "Worker launch template id (GOLOAD_AWS_LAUNCH_TEMPLATE_ID)."
  value       = aws_launch_template.worker.id
}

output "worker_security_group_id" {
  description = "Security group of the workers."
  value       = aws_security_group.worker.id
}

output "api_token_ssm_parameter" {
  description = "Read the UI/API token with: aws ssm get-parameter --with-decryption --name <this>"
  value       = aws_ssm_parameter.api_token.name
}

output "worker_token_ssm_parameter" {
  description = "SSM parameter holding the worker token."
  value       = aws_ssm_parameter.worker_token.name
}
