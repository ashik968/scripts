#!/bin/bash
#Script to remove AMIs with some tags older than 7 days and its associated snapshots.
ami () {
time=`aws ec2 describe-images --image-ids $ami_id --query 'Images[*].[ImageId,CreationDate]' --output text | awk '{ print $2 }'`

time1=`echo $time | head -c 10`

compdate=`date '+%Y-%m-%d' --date='6 days ago'`

flag=$(echo $(( ( $(date -ud $time1 +'%s') - $(date -ud $compdate +'%s') )/60/60/24 )))

echo "flag=$flag"

if [[ "$compdate" > "$time1" ]];

  then
        my_array=( $(aws ec2 describe-images --image-ids $ami_id --output text --query 'Images[*].BlockDeviceMappings[*].Ebs.SnapshotId') )
        echo "my_array=$my_array"
        echo "older than 7 days"
        echo "deregistering old image $ami_id"
        aws ec2 deregister-image --image-id $ami_id
        echo "my_array=$my_array"
        my_array_length=${#my_array[@]}
        echo "Removing Snapshot"
        for (( i=0; i<$my_array_length; i++ ))
        do
                temp_snapshot_id=${my_array[$i]}
                echo "Deleting Snapshot: $temp_snapshot_id"
                aws ec2 delete-snapshot --snapshot-id $temp_snapshot_id
        done
fi

}

owner_id=`aws ec2 describe-security-groups --group-names 'Default' --query 'SecurityGroups[0].OwnerId' --output text`
#Get all AMI IDs with some tags
aws ec2 describe-images --owners $owner_id --filters "Name=tag-value,Values=<some_tags>" --output text --query 'Images[*].{ID:ImageId}' > /root/scripts/AMI/amiid.txt
filename="/tmp/amiid.txt"
while read -r line
do
    ami_id="$line"
    echo "Name read from file - $ami_id"
    ami
done < "$filename"
