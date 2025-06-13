#!/bin/bash
# Script to remove AMIs with specific tags older than 7 days along with associated snapshots

set -euo pipefail

# Define a function to delete AMIs older than 7 days
delete_old_ami() {
    local ami_id="$1"
    local creation_date
    creation_date=$(aws ec2 describe-images --image-ids "$ami_id" --query 'Images[0].CreationDate' --output text | cut -d'T' -f1)

    local threshold_date
    threshold_date=$(date -d '7 days ago' +%F)

    echo "Checking AMI: $ami_id (created on $creation_date)"

    if [[ "$creation_date" < "$threshold_date" ]]; then
        echo "AMI $ami_id is older than 7 days. Proceeding with deregistration."

        # Get associated snapshot IDs
        snapshot_ids=($(aws ec2 describe-images --image-ids "$ami_id" --query 'Images[*].BlockDeviceMappings[*].Ebs.SnapshotId' --output text))

        # Deregister the AMI
        aws ec2 deregister-image --image-id "$ami_id"
        echo "Deregistered AMI: $ami_id"

        # Delete associated snapshots
        for snapshot_id in "${snapshot_ids[@]}"; do
            echo "Deleting snapshot: $snapshot_id"
            aws ec2 delete-snapshot --snapshot-id "$snapshot_id"
        done
    else
        echo "AMI $ami_id is not older than 7 days. Skipping."
    fi
}

# Main logic
owner_id=$(aws sts get-caller-identity --query 'Account' --output text)

# Get list of AMI IDs with specific tag values
ami_list_file="/tmp/amiid.txt"
aws ec2 describe-images \
    --owners "$owner_id" \
    --filters "Name=tag-value,Values=<some_tags>" \
    --query 'Images[*].ImageId' \
    --output text > "$ami_list_file"

# Read each AMI ID and process
while read -r ami_id; do
    [[ -z "$ami_id" ]] && continue
    delete_old_ami "$ami_id"
done < "$ami_list_file"
