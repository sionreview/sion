#!/bin/bash

PREFIX=$1
TYPE=$2
PWD=`dirname $0`
BUCKET="my s3 bucket"

BASE=$PWD/../downloaded/$TYPE/$PREFIX
mkdir -p BASE

aws s3 cp s3://$BUCKET/$TYPE/$PREFIX $BASE --recursive
