output "public_ip" {
  value = aws_eip.this.public_ip
}
output "instance_id" {
  value = aws_instance.this.id
}

# The instance's public IPv6 (empty for IPv4-only nodes). A v4-only node (Local
# Zone) borrows a dual-stack node's v6 as its origin for ipv6-egress packages.
output "public_ipv6" {
  value = try(aws_instance.this.ipv6_addresses[0], "")
}
