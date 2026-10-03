output "instance_id" { value = aws_instance.host.id }
output "public_ip" { value = aws_instance.host.public_ip }
output "dataset_bucket" { value = aws_s3_bucket.datasets.id }
output "backup_bucket" { value = aws_s3_bucket.backups.id }
output "data_volume_id" { value = aws_ebs_volume.data.id }
output "log_group" { value = aws_cloudwatch_log_group.app.name }
output "ssh_tunnel" { value = "ssh -L 18080:127.0.0.1:8080 ubuntu@${aws_instance.host.public_ip}" }
