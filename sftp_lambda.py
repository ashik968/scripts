# -*- coding: utf-8 -*-
"""
===========================================================================
Script Name: sftp_ami_backup.py

Description:
This AWS Lambda-compatible Python script automates the creation of Amazon Machine Images (AMIs)
for EC2 instances tagged with `sftp_server` or `Sftp_Server` in a source region. It performs the following:

1. Detects a flag file in S3 to trigger AMI creation.
2. Creates an AMI of each matching EC2 instance in the primary region.
3. Waits for the AMI to become available.
4. Sends a notification via Amazon SNS upon successful creation.
5. Copies the AMI to a Disaster Recovery (DR) region.
6. Waits for the copied AMI to become available.
7. Deletes the flag file from S3 after successful operations.

Intended Use:
This script is designed to be triggered via AWS Lambda, based on the presence of a flag file
in an S3 bucket. It is used for maintaining SFTP server backups across two regions.

Requirements:
- Python 3.x
- IAM role or credentials with permissions for EC2, S3, and SNS
- `boto3` library
- `snsclient.publish` must reference a valid SNS Topic ARN

Inputs:
- S3 bucket: `im-sftp1`
- Flag files:
    - `spotlighttms/us-west-1/flagfile`
    - `spotlighttms/us-west-2/flagfile`
- Regions: `us-west-1`, `us-west-2` (can be modified)
- SNS Topic ARN: Must be specified

Output:
- AMIs created and copied to DR region
- SNS notification sent
- Flag file deleted from S3

Author: [Your Name]
Created On: [Date]
===========================================================================

"""

import boto3
import datetime
import logging
import time

# Setup logging
logging.basicConfig(level=logging.INFO, format='%(asctime)s [%(levelname)s] %(message)s')

s3 = boto3.resource('s3')
bucket_name = "im-sftp1"
bucket = s3.Bucket(bucket_name)

SNS_TOPIC_ARN = "<Your SNS topic arn>"  # Replace with your SNS Topic ARN


def wait_for_image_availability(ec2_resource, image_id, max_wait=600, interval=30):
    logging.info(f"Waiting for AMI {image_id} to become available...")
    elapsed = 0
    while elapsed < max_wait:
        state = ec2_resource.Image(image_id).state
        if state == 'available':
            logging.info(f"AMI {image_id} is now available.")
            return True
        time.sleep(interval)
        elapsed += interval
    logging.warning(f"Timed out waiting for AMI {image_id} to become available.")
    return False


def create_and_copy_ami(primary_region, dr_region, key):
    ec = boto3.client('ec2', region_name=primary_region)
    ec2_resource = boto3.resource('ec2', region_name=primary_region)

    reservations = ec.describe_instances(Filters=[
        {'Name': 'tag-key', 'Values': ['sftp_server', 'Sftp_Server']},
    ]).get('Reservations', [])

    instances = [i for r in reservations for i in r['Instances']]
    logging.info(f"Found {len(instances)} instances in {primary_region} with SFTP tags.")

    for instance in instances:
        instance_id = instance['InstanceId']
        timestamp = datetime.datetime.now().strftime('%Y-%m-%d_%H-%M')
        image_name = f"SFTP_AMI - {instance_id} from {timestamp}"
        description = f"Lambda created AMI of instance {instance_id} from {timestamp}"

        try:
            logging.info(f"Creating AMI for instance {instance_id} in {primary_region}...")
            response = ec.create_image(
                InstanceId=instance_id,
                Name=image_name,
                Description=description,
                NoReboot=True
            )
            source_ami_id = response['ImageId']

            if not wait_for_image_availability(ec2_resource, source_ami_id):
                logging.error(f"AMI {source_ami_id} creation timed out.")
                continue

            boto3.client('sns', region_name=primary_region).publish(
                TopicArn=SNS_TOPIC_ARN,
                Message=f"SFTP AMI {source_ami_id} created in {primary_region}"
            )

            logging.info(f"Copying AMI {source_ami_id} to {dr_region}...")
            copy_response = boto3.client('ec2', region_name=dr_region).copy_image(
                Name=image_name,
                SourceImageId=source_ami_id,
                SourceRegion=primary_region
            )

            copied_image_id = copy_response['ImageId']
            dr_ec2_resource = boto3.resource('ec2', region_name=dr_region)

            if wait_for_image_availability(dr_ec2_resource, copied_image_id):
                logging.info(f"Copied AMI {copied_image_id} is available in {dr_region}")
            else:
                logging.warning(f"Copied AMI {copied_image_id} did not become available in time.")

            s3.Object(bucket_name, key).delete()
            logging.info(f"Removed flag file: {key}")
        except Exception as e:
            logging.error(f"Error processing instance {instance_id}: {str(e)}")


def lambda_handler(event, context):
    flagfiles = {
        'us-west-1': 'spotlighttms/us-west-1/flagfile',
        'us-west-2': 'spotlighttms/us-west-2/flagfile'
    }

    for region, key in flagfiles.items():
        objs = list(bucket.objects.filter(Prefix=key))
        if any(obj.key == key for obj in objs):
            logging.info(f"Flag file found: {key}")
            dr_region = 'us-west-2' if region == 'us-west-1' else 'us-west-1'
            create_and_copy_ami(primary_region=region, dr_region=dr_region, key=key)
        else:
            logging.info(f"Flag file not found: {key}")
