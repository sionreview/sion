# SION: Elastic Serverless Cloud Storage

**SION** is an elastic, high-performance, cost-effective cloud storage that is built atop ephemeral cloud functions.

## Prepare

- ### EC2 Proxy

  Amazon EC2 AMI: ubuntu-xenial-18.04

  Golang version: 1.16

  Be sure the port **6378 - 6379** is avaiable on the proxy

  We recommend that EC2 proxy and Lambda functions are under the same VPC network, and deploy Sion on a EC2 instance with high bandwidth (`c5n` family will be a good choice).

- ### Golang install

  Jump to [install_go.md](https://github.com/sionreview/sion/blob/master/install_go.md)

- ### Package install

  Install basic package
  ```shell
  sudo apt-get update
  sudo apt-get -y upgrade
  sudo apt install awscli
  sudo apt install zip
  ```

  Clone this repo
  ```go
  git clone https://github.com/sionreview/sion.git
  ```

  Run `aws configure` to setup your AWS credential.

  ```shell
  aws configure
  ```

- ### Lambda Runtime

  #### Lambda Role setup

  Go to AWS IAM console and create a role for the lambda cache node (Lambda function).

  AWS IAM console -> Roles -> Create Role -> Lambda ->

  **`AWSLambdaFullAccess, `**

  **`AWSLambdaVPCAccessExecutionRole, `**

  **`AWSLambdaENIManagementAccess`**

  #### Enable Lambda internet access under VPC

  Plese [refer to this article](https://aws.amazon.com/premiumsupport/knowledge-center/internet-access-lambda-function/). (You could skip this step if you do not want to run InfiniCache under VPC).

  We prepared scripts to help you create/delete NAT gateway in the "deploy" folder.

- ### S3

  Create the S3 bucket to store the zip file of the Lambda code and data output from Lambda functions. Remember the name of this bucket for the configuration in next step.

- ### Configuration

  #### Lambda function create and config

  Edit `deploy/create_function.sh` and `deploy/update_function.sh`
  ```shell
  DEPLOY_PREFIX="your lambda function prefix"
  DEPLOY_CLUSTER=1000 # The number of Lambda deployments used for window rotation.
  DEPLOY_MEM=1536 # The memory of Lambda deployments.
  S3="your bucket name"
  ```

  Edit destination S3 bucket in `lambda/config.go`, these buckets are for data collection and durable storage.
  ```go
  S3_COLLECTOR_BUCKET = "your data collection bucket"
  S3_BACKUP_BUCKET = "your COS bucket%s"  // Leave %s at the end your COS bucket.
  ```

  Edit the aws settings and the VPC configuration in `deploy/deploy_function.go`. If you do not want to run InfiniCache under VPC, you do not need to modify the `subnet` and `securityGroup` settings.

  ```go
  ROLE = "arn:aws:iam::[aws account id]:role/[role name]"
  REGION = "us-east-1"
  ...
  ...
  subnet = []*string{
    aws.String("your subnet 1"),
    aws.String("your subnet 2"),
  }
  securityGroup = []*string{
    aws.String("your security group")
  }
  ```

  Run script to create and deploy lambda functions (Also, if you do not want to run InfiniCache under VPC, 
  you need to remove the `--no-vpc` flag on executing `deploy/create_function.sh`).

  ```shell
  export GO111MODULE="on"
  go get
  deploy/create_function.sh --no-vpc 600
  ```

  #### Proxy configuration

  Edit `proxy/config/config.go`, change the aws region, deployment size, and prefix of the Lambda functions.
  ```go
  const AWSRegion = "us-east-1"
  const LambdaMaxDeployments = 1000 // Number of lambda for window rotation.
  const LambdaPrefix = "Your Lambda Function Prefix"
  const ServerPublicIp = ""  // Leave it empty if using VPC.
  ```

## Execution

- Proxy server

  Run `make start` to start proxy server.  `make start` would print nothing to the console. If you want to check the log message, you need to set the `debug` flag to be `true` in the `proxy/proxy.go`.

  ```bash
  make start
  ```

  To stop proxy server, run `make stop`. If `make stop` is not working, you could use `pgrep proxy`, `pgrep go` to find the pid, and check the `infinicache pid` and kill them.

- Client library

  The toy demo for Client Library

  ```bash
  go run client/example/main.go
  ```

  The result should be

  ```bash
  ~$ go run client/example/main.go
  2020/03/08 05:05:19 EcRedis Set foo 14630930
  2020/03/08 05:05:19 EcRedis Got foo 3551124 ( 2677371 865495 )
  ```

- Stand-alone local simulation

  Enable local function execution by editing `lambda/config.go`:
  ```go
  DRY_RUN = true
  ```

  Run `make start-local` to start a stand-alone local proxy server, which will invoke functions locally to simulation Lambda execution.

  Run `make test` to put/get a toy object.

## Related repo

Workload replayer [sionreplayer](https://github.com/sionreview/sionreplayer)