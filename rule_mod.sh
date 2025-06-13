#!/bin/bash
#================================================================
# DESCRIPTION:
# Replicates CloudWatch EventBridge rules from one region/env to another.
#
# Script usage:
# ./script.sh -a <source-region> -b <destination-region> -c <source-env> -d <destination-env>
#
# Only rules containing the source env suffix are copied if -c is given.
#================================================================

set -euo pipefail

usage() {
    echo "Usage: $0 -a <source-region> -b <destination-region> -c <source-env (optional)> -d <destination-env>"
    exit 1
}

# Parse input arguments
while getopts "a:b:c:d:" opt; do
    case "$opt" in
        a) source_region="$OPTARG" ;;
        b) dest_region="$OPTARG" ;;
        c) source_env="$OPTARG" ;;
        d) dest_env="$OPTARG" ;;
        *) usage ;;
    esac
done

# Validate required arguments
if [[ -z "${source_region:-}" || -z "${dest_region:-}" || -z "${dest_env:-}" ]]; then
    echo "Error: Missing required arguments."
    usage
fi

# Fetch all rules from the source region
all_rules_json=$(aws events list-rules --region "$source_region" --output json)
rules_count=$(echo "$all_rules_json" | jq '.Rules | length')

if [[ "$rules_count" -eq 0 ]]; then
    echo "No rules found in source region ($source_region). Exiting."
    exit 0
fi

# Helper to check if rule exists in destination and clean up old targets
rule_cleanup_if_exists() {
    local rule_name="$1"
    if aws events describe-rule --name "$rule_name" --region "$dest_region" &>/dev/null; then
        echo "Rule $rule_name exists in $dest_region. Cleaning up old targets..."
        old_target_ids=$(aws events list-targets-by-rule --rule "$rule_name" --region "$dest_region" | jq -r '.Targets[].Id')
        if [[ -n "$old_target_ids" ]]; then
            aws events remove-targets --rule "$rule_name" --ids $old_target_ids --region "$dest_region"
        fi
    else
        echo "Rule $rule_name does not exist in $dest_region."
    fi
}

# Process each rule
for (( i = 0; i < rules_count; i++ )); do
    rule=$(echo "$all_rules_json" | jq ".Rules[$i]")
    name=$(echo "$rule" | jq -r ".Name")
    desc=$(echo "$rule" | jq -r ".Description")
    state=$(echo "$rule" | jq -r ".State")
    sched=$(echo "$rule" | jq -r ".ScheduleExpression")

    # If source_env is set, only process matching rule names
    if [[ -n "${source_env:-}" && "$name" != *"$source_env"* ]]; then
        continue
    fi

    echo "Processing rule: $name"

    # Get targets and modify if needed
    targets_json=$(aws events list-targets-by-rule --rule "$name" --region "$source_region")
    targets=$(echo "$targets_json" | jq '.Targets')

    if [[ "$targets" == "[]" ]]; then
        echo "No targets for rule $name. Skipping."
        continue
    fi

    # Optionally rewrite URLs in targets
    mod_targets=$(echo "$targets" | sed "s/$source_region/$dest_region/g" | sed "s/app1-${source_env:-}/app01-${dest_env}/g")

    new_rule_name="${name}_${dest_env}"
    rule_cleanup_if_exists "$new_rule_name"

    echo "Creating rule $new_rule_name in $dest_region..."
    aws events put-rule \
        --name "$new_rule_name" \
        --schedule-expression "$sched" \
        --description "$desc" \
        --state "$state" \
        --region "$dest_region"

    echo "Adding targets to rule $new_rule_name..."
    aws events put-targets \
        --rule "$new_rule_name" \
        --region "$dest_region" \
        --targets "$mod_targets"

    echo "Finished rule: $name -> $new_rule_name"
    echo "-------------------------------"
done
