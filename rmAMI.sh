#!/bin/bash
#
# Description:
#   This script deregisters AMIs and deletes their associated snapshots based on
#   tags and age. It is a safer and more flexible alternative to the original
#   script.
#
# Features:
#   - Deletes AMIs older than a specified number of days.
#   - Filters AMIs based on a specific tag key-value pair.
#   - Includes a dry-run mode to preview changes before execution.
#   - Accepts command-line arguments for region, tag, age, and dry-run mode.
#   - Uses 'jq' for efficient parsing of AWS CLI JSON output.
#
# Dependencies:
#   - aws-cli: The AWS Command Line Interface
#   - jq: A lightweight and flexible command-line JSON processor
#
# Usage:
#   ./rmAMI.sh -r <aws_region> -t <tag_key> -v <tag_value> -d <days> [--dry-run]
#
# Examples:
#   # Dry run: show what would be deleted in us-east-1 for AMIs tagged
#   # with 'Name=my-ami' and older than 30 days.
#   ./rmAMI.sh -r us-east-1 -t Name -v my-ami -d 30 --dry-run
#
#   # Delete all AMIs in us-west-2 tagged with 'env=prod' and older than 7 days.
#   ./rmAMI.sh -r us-west-2 -t env -v prod -d 7
#

set -euo pipefail

# Global counters for summary
amis_deleted=0
snapshots_deleted=0

# Function: usage
# Description: Displays help text for the script.
usage() {
    echo "Usage: $0 -r <aws_region> -t <tag_key> -v <tag_value> -d <days> [--dry-run]"
    echo "  -r: AWS region"
    echo "  -t: Tag key to filter AMIs"
    echo "  -v: Tag value to filter AMIs"
    echo "  -d: Age in days for AMIs to be deleted"
    echo "  --dry-run: (Optional) If specified, the script will only list the AMIs and snapshots to be deleted."
    exit 1
}

# Function to process a single AMI
process_ami() {
    local ami_id="$1"
    local creation_date="$2"
    local snapshot_ids_str="$3"
    local days_old="$4"
    local dry_run="$5"
    local region="$6"

    local threshold_date
    threshold_date=$(date -d "$days_old days ago" +%s)
    local creation_date_epoch
    creation_date_epoch=$(date -d "$creation_date" +%s)

    echo "Checking AMI: $ami_id (created on $creation_date)"

    if [[ "$creation_date_epoch" -lt "$threshold_date" ]]; then
        echo "AMI $ami_id is older than $days_old days. Proceeding with deregistration."

        read -ra snapshot_ids <<< "${snapshot_ids_str//[$'\t\r\n']}"


        if [ "$dry_run" = true ]; then
            echo "[Dry Run] Would deregister AMI: $ami_id"
            amis_deleted=$((amis_deleted + 1))
            for snapshot_id in "${snapshot_ids[@]}"; do
                if [ -n "$snapshot_id" ]; then
                    echo "[Dry Run] Would delete snapshot: $snapshot_id"
                    snapshots_deleted=$((snapshots_deleted + 1))
                fi
            done
        else
            # Deregister the AMI
            if aws ec2 deregister-image --image-id "$ami_id" --region "$region"; then
                echo "Deregistered AMI: $ami_id"
                amis_deleted=$((amis_deleted + 1))

                # Delete associated snapshots
                for snapshot_id in "${snapshot_ids[@]}"; do
                    if [ -n "$snapshot_id" ]; then
                        if aws ec2 delete-snapshot --snapshot-id "$snapshot_id" --region "$region"; then
                            echo "Deleted snapshot: $snapshot_id"
                            snapshots_deleted=$((snapshots_deleted + 1))
                        else
                            echo "Error: Failed to delete snapshot $snapshot_id" >&2
                        fi
                    fi
                done
            else
                echo "Error: Failed to deregister AMI $ami_id" >&2
            fi
        fi
    else
        echo "AMI $ami_id is not older than $days_old days. Skipping."
    fi
}

# Function to fetch and process AMIs
fetch_and_process_amis() {
    local region="$1"
    local tag_key="$2"
    local tag_value="$3"
    local days_old="$4"
    local dry_run="$5"

    owner_id=$(aws sts get-caller-identity --query 'Account' --output text --region "$region")

    # Fetch AMIs with the specified tag
    images_json=$(aws ec2 describe-images \
        --owners "$owner_id" \
        --filters "Name=tag:$tag_key,Values=$tag_value" \
        --query 'Images[*].{ImageId:ImageId, CreationDate:CreationDate, SnapshotIds:BlockDeviceMappings[*].Ebs.SnapshotId}' \
        --output json --region "$region")

    if [ -z "$images_json" ] || [ "$images_json" == "[]" ]; then
        echo "No AMIs found with tag $tag_key=$tag_value."
        return
    fi

    # Process each image using jq
    echo "$images_json" | jq -c '.[]' | while read -r image_json; do
        if [ -n "$image_json" ]; then
            ami_id=$(echo "$image_json" | jq -r '.ImageId')
            creation_date=$(echo "$image_json" | jq -r '.CreationDate' | cut -d'T' -f1)
            snapshot_ids=$(echo "$image_json" | jq -r '.SnapshotIds | @tsv')

            process_ami "$ami_id" "$creation_date" "$snapshot_ids" "$days_old" "$dry_run" "$region"
        fi
    done
}

# Main logic
main() {
    # Default values
    dry_run=false

    # Parse command-line arguments
    while [[ "$#" -gt 0 ]]; do
        case $1 in
            -r) region="$2"; shift ;;
            -t) tag_key="$2"; shift ;;
            -v) tag_value="$2"; shift ;;
            -d) days_old="$2"; shift ;;
            --dry-run) dry_run=true ;;
            *) usage ;;
        esac
        shift
    done

    # Validate that all required arguments are provided
    if [ -z "${region:-}" ] || [ -z "${tag_key:-}" ] || [ -z "${tag_value:-}" ] || [ -z "${days_old:-}" ]; then
        usage
    fi

    # Check for dependencies
    if ! command -v jq &> /dev/null; then
        echo "Error: jq is not installed. Please install jq to continue." >&2
        exit 1
    fi

    if ! command -v aws &> /dev/null; then
        echo "Error: aws-cli is not installed. Please install aws-cli to continue." >&2
        exit 1
    fi

    echo "Starting AMI cleanup process..."
    echo "---------------------------------"
    echo "Region: $region"
    echo "Tag: $tag_key=$tag_value"
    echo "Older than: $days_old days"
    echo "Dry run: $dry_run"
    echo "---------------------------------"

    fetch_and_process_amis "$region" "$tag_key" "$tag_value" "$days_old" "$dry_run"

    echo "---------------------------------"
    echo "Summary:"
    if [ "$dry_run" = true ]; then
        echo "  AMIs that would be deregistered: $amis_deleted"
        echo "  Snapshots that would be deleted: $snapshots_deleted"
    else
        echo "  AMIs deregistered: $amis_deleted"
        echo "  Snapshots deleted: $snapshots_deleted"
    fi
    echo "---------------------------------"
}

# Call the main function
main "$@"
