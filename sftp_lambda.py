# -*- coding: utf-8 -*-
from __future__ import unicode_literals
import boto3
import collections
import datetime
import sys
import pprint
import time
import subprocess

# Function definition is here


def ami(primary_region, dr_region, key):
    ec = boto3.client('ec2', region_name=primary_region)
    # Describe details of instances with the given tags

    reservations = ec.describe_instances(
        Filters=[
            {'Name': 'tag-key', 'Values': ['sftp_server', 'Sftp_Server']},
        ]
    ).get(
        'Reservations', []
    )
    # Getting number of instances needs to take backup
    instances = sum([[i for i in r['Instances']]
                     for r in reservations], [])
    print "primary_region %s" % (primary_region)
    print "Found %d instances that needs backing up" % len(instances)

    for instance in instances:
        create_time = datetime.datetime.now()
        create_fmt = create_time.strftime('%Y-%m-%d_%I-%M')
        print "Starting AMI creation in %s at %s" % (primary_region, create_fmt)
        AMIid = ec.create_image(InstanceId=instance['InstanceId'], Name="SFTP_AMI - " + instance['InstanceId'] + " from " + create_fmt,
                                Description="Lambda created AMI of instance " + instance['InstanceId'] + " from " + create_fmt, NoReboot=True, DryRun=False)
        ec2 = boto3.resource('ec2', region_name=primary_region)
        SourceImageId = AMIid['ImageId']
        state = ec2.Image(SourceImageId).state
        print("current state of AMI creation %s") % (state)
        print("Waiting 2 minutes for completing the AMI creation")
        time.sleep(90)
        while "available" not in state:
            time.sleep(60)
            state = ec2.Image(SourceImageId).state
        else:
            snsclient = boto3.client('sns', region_name="us-west-1")
            msg = "SFTP AMI %s created in %s region" % (SourceImageId, primary_region) 
            response = snsclient.publish(TopicArn="<Your SNS topic arn>", Message=msg)
            state = ec2.Image(SourceImageId).state
            print "Completed AMI creation in %s" % (primary_region)
            copy_ec2_connect = boto3.client('ec2', region_name=dr_region)
            print("Starting copying AMI from %s to %s ") % (
                primary_region, dr_region)
            response = copy_ec2_connect.copy_image(Name="SFTP_AMI - " + instance[
                                                   'InstanceId'] + " from " + create_fmt, SourceImageId=AMIid['ImageId'], SourceRegion=primary_region)
            print("Waiting 2 minutes for completing the AMI copying")
            time.sleep(90)
            ec2_cp = boto3.resource('ec2', region_name=dr_region)
            COPYImageId = response['ImageId']
            copystate = ec2_cp.Image(COPYImageId).state
            print(" Current AMI copy status to %s is %s . It will take 3-4 minutes to complete.") % (dr_region, copystate)
            obj = s3.Object(bucket_name, key)
            print("Removing flagfile from the S3 bucket")
            obj.delete()
            return
s3 = boto3.resource('s3')
bucket_name="im-sftp1" 
bucket = s3.Bucket(bucket_name)      
def lambda_handler(event, context):
    key1 = 'spotlighttms/us-west-1/flagfile'
    key2 = 'spotlighttms/us-west-2/flagfile'
    objs1 = list(bucket.objects.filter(Prefix=key1))
    objs2 = list(bucket.objects.filter(Prefix=key2))
    if len(objs1) > 0 and objs1[0].key == key1:
        print("%s Exists in us-west-1") % (key1)
        # Calling ami function
        ami(primary_region='us-west-1', dr_region='us-west-2', key=key1 )       
    else:
        print('File %s not found') % (key1)
    if len(objs2) > 0 and objs2[0].key == key2:
        print("%s Exists!") % (key2)
        # Calling ami function
        ami(primary_region='us-west-2', dr_region='us-west-1', key=key2 )
    else:
        print('File %s not found') % (key2)
