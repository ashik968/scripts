#!/bin/bash
#================================================================
# DESCRIPTION
#
# Script inputs
# -a --> source region. eg:- us-east-1
# -b --> destination region. eg:- us-west-2 [Optional Input]
# -c --> source environment. eg:- prod
# -d --> destination environment. eg:- qa14
#
# Script execution syntax:-  ./script -a <source region> -b <destination region> -c <source environment> -d <destination environment>
# eg:- ./script -a us-east-1 -b -us-west-2 -c prod -d qa14
#
# Script working
# 
# The 3rd parameter of the script is optional. If we provide an input for 3rd parameter, let's say "prod".
# Then, only the cloudwatch rules with "prod" suffix will be executed.
# If we didn't provide input for 3rd parameter, then all the cloudwatch rules in the source region are executed by the script.
# 
#================================================================
if [[ -x "$(basename "$0")" ]];then
    echo ""
else
    echo "File '$(basename "$0")' is not executable. Please set proper permissions"
    exit 1
fi

set -e;

while getopts a:b:c:d: option
do
    case "${option}"
        in
        a) source_region=${OPTARG};;
        b) dest_region=${OPTARG};;
        c) source_env=${OPTARG};;
        d) dest_env=${OPTARG};;
    esac
done

if [ -z "$source_region" ] || [ -z "$dest_region" ] || [ -z "$dest_env" ];then
    echo -e "\n Source_region, dest_region and dest_env parameters are needed. Please check and try again. \n"
    exit 1
fi


#Get list of aws rules in source region into the variable
full_lists=`aws events list-rules --region $source_region --output json`
length=$(echo $full_lists | jq '.Rules | length')
echo "length=$length"
#Funtion for checking whether the rule already exists in the destination region.
rule_exists() {
    check_rule_exists=`aws events describe-rule --name "${Name}_${dest_env}" --region $dest_region 2>/dev/null`
    if [[ $check_rule_exists = *"ResourceNotFoundException"* ]]; then
        echo "Rule ${Name} doesn't exists in $dest_region"
    else
        #Remove targets of old rule in DR region
        for j in $region2; do
            echo "j=$j"
            aws events remove-targets --rule "${Name}_${dest_env}" --ids "$j" --region ${dest_region} 2>/dev/null
        done
    fi
}

execute() {
    i=0
    while [  $i -lt $length ]; do
        echo "i=$i"
        echo "--------------------------"
        current_list=`echo "$full_lists" | jq ".Rules[$i]"`
        #Parsing required data from json file.
        ScheduleExpression=$(echo "$current_list" | jq -r ".ScheduleExpression")
        echo "ScheduleExpression=$ScheduleExpression"
        Description=`echo $current_list | jq -r ".Description"`
        Name=`echo $current_list | jq -r ".Name"`
        State=`echo $current_list | jq -r ".State"`
        echo "Name=$Name"
        input=`aws events list-targets-by-rule --rule "${Name}" --output json --region ${source_region} --output json | jq -r .Targets[] 2>/dev/null | sed "s/$source_region/$dest_region/g" `
        url=`echo "$input" | grep -Eo "(https)://[a-zA-Z0-9./?=_-]*" | uniq | awk 'NR==1'`
        echo "url=$url"
        
        #Filter server name from the target input
        server=`echo "$input" | grep -Eo "(https)://[a-zA-Z0-9./?=_-]*" | uniq | sed 's/.*app1.//' | sed "s/.com//g" | sed '/https/d' | cut -f1 -d"."`
        
        echo "Server=$server"
        mod_url=`echo $url | sed "s,$server,$dest_env,g"`
        echo "mod_url=$mod_url"
        mod_input=`echo $input | sed "s,app1-${server},app01-${dest_env},g"`
        
        #Get target ID's for comparison
        region1=`aws events list-targets-by-rule --rule "${Name}" --region ${source_region} --output json 2>/dev/null | jq -r '.Targets[].Id'`
        region2=`aws events list-targets-by-rule --rule "${Name}_${dest_env}" --region ${dest_region} --output json 2>/dev/null | jq -r '.Targets[].Id'`
        echo "region1=$region1"
        echo "region2=$region2"
        #Compare target ID's
        if [ "${region1}" = "${region2}" ];then
            echo "No Difference"
            rule_exists
        else
            echo "Difference detected"
            
        fi
        
        if [ -z "${input}" ];then
            echo "__"
            
        else
            #Storing values needed to create rule in the variables
            echo "Name=$Name"
            server_name="${Name}_${dest_env}"
            #Create rule with modified name(include server name in the rule name)
            aws events put-rule --name "${server_name}" --schedule-expression "$ScheduleExpression" --description "$Description" --state "$State" --region $dest_region
            for row in $(echo "${mod_input}" | jq -r '. | @base64'); do
                _jq() {
                    echo ${row} | base64 --decode | jq -r ${1}
                }
                
                value=`echo $(_jq '.')`
                #Create rule targets in the DR region
                aws events put-targets --rule "${server_name}" --region $dest_region --targets "${mod_input}"
                rule_exists
            done
        fi
        echo "========="
        let i=i+1
    done
}

current_list=`echo "$full_lists" | jq ".Rules[$i]"`
#Parsing required data from json output.
ScheduleExpression=$(echo "$current_list" | jq -r ".ScheduleExpression")
echo "ScheduleExpression=$ScheduleExpression"
Description=`echo $current_list | jq -r ".Description"`
Name=`echo $current_list | jq -r ".Name"`
State=`echo $current_list | jq -r ".State"`



if [[ $length == 0 ]];then
    
    echo "No rules found in $source_region. Please check and try again"
    exit 0
    
else
    
    if [ -z "$source_env" ];then
        echo "source_env is empty"
        execute
        
    else
            #This code section works only if the source env value is given.
            echo "Name=$Name"
            echo "Source env found"
                i=0
                while [  $i -lt $length ]; do
                    echo "i=$i"
                    echo "--------------------------"
                    current_list=`echo "$full_lists" | jq ".Rules[$i]"`
                    #Parsing required data from json file.
                    ScheduleExpression=$(echo "$current_list" | jq -r ".ScheduleExpression")
                    echo "ScheduleExpression=$ScheduleExpression"
                    Description=`echo $current_list | jq -r ".Description"`
                    Name=`echo $current_list | jq -r ".Name"`
                    State=`echo $current_list | jq -r ".State"`
                    echo "Name=$Name"
                    echo "source_env=$source_env"
                    if [[ $Name == *"$source_env"* ]] || [[ $Name =~ *"$source_env"* ]]; then

                        input=`aws events list-targets-by-rule --rule "${Name}" --output json --region ${source_region} --output json | jq -r .Targets[] 2>/dev/null | sed "s/$source_region/$dest_region/g" `
                        url=`echo "$input" | grep -Eo "(https)://[a-zA-Z0-9./?=_-]*" | uniq | awk 'NR==1'`
                        echo "url=$url"
                        
                        #Filter server name from the target input
                        server=`echo "$input" | grep -Eo "(https)://[a-zA-Z0-9./?=_-]*" | uniq | sed 's/.*app1.//' | sed "s/.com//g" | sed '/https/d' | cut -f1 -d"."`
                        
                        echo "Server=$server"
                        mod_url=`echo $url | sed "s,$server,$dest_env,g"`
                        echo "mod_url=$mod_url"
                        mod_input=`echo $input | sed "s,app1-${server},app01-${dest_env},g"`
                        
                        #Get target ID's for comparison
                        region1=`aws events list-targets-by-rule --rule "${Name}" --region ${source_region} --output json 2>/dev/null | jq -r '.Targets[].Id'`
                        region2=`aws events list-targets-by-rule --rule "${Name}_${dest_env}" --region ${dest_region} --output json 2>/dev/null | jq -r '.Targets[].Id'`
                        echo "region1=$region1"
                        echo "region2=$region2"
                        #Compare target ID's
                        if [ "${region1}" = "${region2}" ];then
                            echo "No Difference"
                            rule_exists
                        else
                            echo "Difference detected"
                            
                        fi
                    
                        if [ -z "${input}" ];then
                            echo "__"
                            
                        else
                            #Storing values needed to create rule in the variables
                            echo "Name=$Name"
                            server_name="${Name}_${dest_env}"
                            #Create rule with modified name(include server name in the rule name)
                            aws events put-rule --name "${server_name}" --schedule-expression "$ScheduleExpression" --description "$Description" --state "$State" --region $dest_region
                            for row in $(echo "${mod_input}" | jq -r '. | @base64'); do
                                _jq() {
                                    echo ${row} | base64 --decode | jq -r ${1}
                                }
                                
                                value=`echo $(_jq '.')`
                                #Create rule targets in the DR region
                                aws events put-targets --rule "${server_name}" --region $dest_region --targets "${mod_input}"
                                rule_exists
                            done
                        fi
                    fi
                    echo "========="
                    let i=i+1
                done       
    fi
fi

