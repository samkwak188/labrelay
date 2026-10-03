data "aws_caller_identity" "current" {}
data "aws_ssm_parameter" "ubuntu" {
  name = "/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id"
}
resource "aws_vpc" "pilot" { cidr_block = "10.77.0.0/16" }
resource "aws_subnet" "pilot" {
  vpc_id                  = aws_vpc.pilot.id
  cidr_block              = "10.77.1.0/24"
  availability_zone       = "us-east-2a"
  map_public_ip_on_launch = true
}
resource "aws_internet_gateway" "pilot" { vpc_id = aws_vpc.pilot.id }
resource "aws_route_table" "pilot" {
  vpc_id = aws_vpc.pilot.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.pilot.id
  }
}
resource "aws_route_table_association" "pilot" {
  subnet_id      = aws_subnet.pilot.id
  route_table_id = aws_route_table.pilot.id
}
resource "aws_vpc_endpoint" "s3" {
  vpc_id            = aws_vpc.pilot.id
  service_name      = "com.amazonaws.us-east-2.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = [aws_route_table.pilot.id]
}
resource "aws_security_group" "pilot" {
  name   = var.name
  vpc_id = aws_vpc.pilot.id
  ingress {
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = [var.ssh_cidr]
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}
resource "aws_key_pair" "operator" {
  key_name   = var.name
  public_key = var.ssh_public_key
}
resource "aws_s3_bucket" "datasets" {
  bucket        = "${var.name}-${data.aws_caller_identity.current.account_id}-datasets"
  force_destroy = false
  lifecycle { prevent_destroy = true }
}
resource "aws_s3_bucket_versioning" "datasets" {
  bucket = aws_s3_bucket.datasets.id
  versioning_configuration { status = "Enabled" }
}
resource "aws_s3_bucket" "backups" {
  bucket        = "${var.name}-${data.aws_caller_identity.current.account_id}-backups"
  force_destroy = false
  lifecycle { prevent_destroy = true }
}
resource "aws_s3_bucket_lifecycle_configuration" "backups" {
  bucket = aws_s3_bucket.backups.id
  rule {
    id     = "seven-days"
    status = "Enabled"
    filter { prefix = "catalog/" }
    expiration { days = 7 }
  }
}
locals { buckets = { datasets = aws_s3_bucket.datasets, backups = aws_s3_bucket.backups } }
resource "aws_s3_bucket_public_access_block" "private" {
  for_each                = local.buckets
  bucket                  = each.value.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
resource "aws_s3_bucket_server_side_encryption_configuration" "encrypted" {
  for_each = local.buckets
  bucket   = each.value.id
  rule {
    apply_server_side_encryption_by_default { sse_algorithm = "AES256" }
  }
}
resource "aws_s3_bucket_policy" "tls" {
  for_each = local.buckets
  bucket   = each.value.id
  policy   = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Deny", Principal = "*", Action = "s3:*", Resource = [each.value.arn, "${each.value.arn}/*"], Condition = { Bool = { "aws:SecureTransport" = "false" } } }] })
}
resource "aws_iam_role" "host" {
  name               = var.name
  assume_role_policy = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Principal = { Service = "ec2.amazonaws.com" }, Action = "sts:AssumeRole" }] })
}
resource "aws_iam_role_policy" "host" {
  role = aws_iam_role.host.id
  policy = jsonencode({ Version = "2012-10-17", Statement = [
    { Effect = "Allow", Action = ["s3:ListBucket", "s3:GetBucketVersioning", "s3:ListBucketMultipartUploads", "s3:ListBucketVersions"], Resource = aws_s3_bucket.datasets.arn },
    { Effect = "Allow", Action = ["s3:GetObject", "s3:GetObjectVersion", "s3:PutObject", "s3:DeleteObject", "s3:DeleteObjectVersion", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"], Resource = "${aws_s3_bucket.datasets.arn}/uploads/*" },
    { Effect = "Allow", Action = ["s3:ListBucket"], Resource = aws_s3_bucket.backups.arn },
    { Effect = "Allow", Action = ["s3:PutObject", "s3:GetObject"], Resource = "${aws_s3_bucket.backups.arn}/catalog/*" },
    { Effect = "Allow", Action = ["logs:CreateLogStream", "logs:PutLogEvents"], Resource = "${aws_cloudwatch_log_group.app.arn}:*" },
    { Effect = "Allow", Action = ["cloudwatch:PutMetricData"], Resource = "*", Condition = { StringEquals = { "cloudwatch:namespace" = "LabRelay" } } }
  ] })
}
resource "aws_iam_instance_profile" "host" {
  name = var.name
  role = aws_iam_role.host.name
}
resource "aws_instance" "host" {
  ami                    = data.aws_ssm_parameter.ubuntu.value
  instance_type          = "t3.small"
  subnet_id              = aws_subnet.pilot.id
  vpc_security_group_ids = [aws_security_group.pilot.id]
  key_name               = aws_key_pair.operator.key_name
  iam_instance_profile   = aws_iam_instance_profile.host.name
  credit_specification { cpu_credits = "standard" }
  metadata_options {
    http_tokens                 = "required"
    http_put_response_hop_limit = 2
  }
  root_block_device {
    volume_type = "gp3"
    volume_size = 20
    encrypted   = true
  }
  user_data = <<-EOT
    #!/bin/bash
    set -eu
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io docker-compose-v2 awscli python3 curl
    systemctl enable --now docker
    install -d -m 0750 /opt/labrelay /srv/labrelay
    usermod -aG docker ubuntu
  EOT
  tags      = { Name = var.name }
}
resource "aws_ebs_volume" "data" {
  availability_zone = aws_subnet.pilot.availability_zone
  size              = 30
  type              = "gp3"
  encrypted         = true
  lifecycle { prevent_destroy = true }
  tags = { Name = "${var.name}-data" }
}
resource "aws_volume_attachment" "data" {
  device_name                    = "/dev/sdf"
  volume_id                      = aws_ebs_volume.data.id
  instance_id                    = aws_instance.host.id
  stop_instance_before_detaching = true
}
resource "aws_cloudwatch_log_group" "app" {
  name              = "/labrelay/${var.name}"
  retention_in_days = 14
}
resource "aws_sns_topic" "alerts" { name = "${var.name}-alerts" }
resource "aws_sns_topic_subscription" "alerts" {
  topic_arn = aws_sns_topic.alerts.arn
  protocol  = "email"
  endpoint  = var.alert_email
}
resource "aws_cloudwatch_metric_alarm" "instance" {
  alarm_name          = "${var.name}-instance-failure"
  namespace           = "AWS/EC2"
  metric_name         = "StatusCheckFailed"
  dimensions          = { InstanceId = aws_instance.host.id }
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = 2
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  alarm_actions       = [aws_sns_topic.alerts.arn]
}
resource "aws_cloudwatch_metric_alarm" "custom" {
  for_each            = { Heartbeat = { threshold = 1, comparison = "LessThanThreshold", periods = 3 }, DiskUsedPercent = { threshold = 85, comparison = "GreaterThanThreshold", periods = 2 }, VerificationStalled = { threshold = 1, comparison = "GreaterThanOrEqualToThreshold", periods = 1 } }
  alarm_name          = "${var.name}-${each.key}"
  namespace           = "LabRelay"
  metric_name         = each.key
  dimensions          = { InstanceId = aws_instance.host.id }
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = each.value.periods
  threshold           = each.value.threshold
  comparison_operator = each.value.comparison
  treat_missing_data  = "breaching"
  alarm_actions       = [aws_sns_topic.alerts.arn]
}
resource "aws_budgets_budget" "pilot" {
  name         = var.name
  budget_type  = "COST"
  limit_amount = tostring(var.monthly_budget_usd)
  limit_unit   = "USD"
  time_unit    = "MONTHLY"
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 80
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.alert_email]
  }
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "FORECASTED"
    subscriber_email_addresses = [var.alert_email]
  }
}
