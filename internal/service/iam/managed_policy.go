package iam

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// awsManagedPolicyPrefix is the ARN prefix of AWS-managed policies, which
// exist in every account without being created.
const awsManagedPolicyPrefix = "arn:aws:iam::aws:policy"

// awsManagedPolicyCreated is the CreateDate reported for AWS-managed
// policies; a fixed instant keeps reads stable.
var awsManagedPolicyCreated = time.Date(2015, time.February, 6, 18, 40, 0, 0, time.UTC)

// awsManagedPolicyDocuments holds the documents of the AWS-managed policies
// most often attached in local stacks. Other AWS-managed policies are still
// attachable; their document is not modeled.
var awsManagedPolicyDocuments = map[string]string{
	"AWSLambdaBasicExecutionRole": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["logs:CreateLogGroup","logs:CreateLogStream","logs:PutLogEvents"],"Resource":"*"}]}`,
	"AWSLambdaVPCAccessExecutionRole": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["logs:CreateLogGroup","logs:CreateLogStream","logs:PutLogEvents",` +
		`"ec2:CreateNetworkInterface","ec2:DescribeNetworkInterfaces","ec2:DescribeSubnets","ec2:DeleteNetworkInterface","ec2:AssignPrivateIpAddresses","ec2:UnassignPrivateIpAddresses"],"Resource":"*"}]}`,
	"AWSLambdaSQSQueueExecutionRole": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sqs:ReceiveMessage","sqs:DeleteMessage","sqs:GetQueueAttributes",` +
		`"logs:CreateLogGroup","logs:CreateLogStream","logs:PutLogEvents"],"Resource":"*"}]}`,
	"AWSXRayDaemonWriteAccess": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["xray:PutTraceSegments","xray:PutTelemetryRecords","xray:GetSamplingRules",` +
		`"xray:GetSamplingTargets","xray:GetSamplingStatisticSummaries"],"Resource":["*"]}]}`,
	"AmazonAPIGatewayPushToCloudWatchLogs": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["logs:CreateLogGroup","logs:CreateLogStream",` +
		`"logs:DescribeLogGroups","logs:DescribeLogStreams","logs:PutLogEvents","logs:GetLogEvents","logs:FilterLogEvents"],"Resource":"*"}]}`,
	"AmazonS3ReadOnlyAccess":   `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:Get*","s3:List*","s3:Describe*","s3-object-lambda:Get*","s3-object-lambda:List*"],"Resource":"*"}]}`,
	"AmazonS3FullAccess":       `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*","s3-object-lambda:*"],"Resource":"*"}]}`,
	"AmazonDynamoDBFullAccess": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["dynamodb:*"],"Resource":"*"}]}`,
	"AmazonSQSFullAccess":      `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sqs:*"],"Resource":"*"}]}`,
	"AmazonSNSFullAccess":      `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sns:*"],"Resource":"*"}]}`,
	"CloudWatchLogsFullAccess": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["logs:*"],"Resource":"*"}]}`,
	"AdministratorAccess":      `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`,
	"ReadOnlyAccess":           `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["*:Describe*","*:Get*","*:List*"],"Resource":"*"}]}`,
}

// awsManagedPolicy builds the AWS-managed policy an ARN such as
// arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole names.
func awsManagedPolicy(arn string) (*Policy, bool) {
	rest, ok := strings.CutPrefix(arn, awsManagedPolicyPrefix+"/")
	if !ok || rest == "" || strings.HasSuffix(rest, "/") {
		return nil, false
	}

	path, name := "/", rest
	if i := strings.LastIndex(rest, "/"); i >= 0 {
		path, name = "/"+rest[:i+1], rest[i+1:]
	}

	sum := sha256.Sum256([]byte(arn))

	return &Policy{
		PolicyName:       name,
		PolicyID:         "ANPA" + strings.ToUpper(hex.EncodeToString(sum[:8])),
		Arn:              arn,
		Path:             path,
		DefaultVersionID: "v1",
		IsAttachable:     true,
		CreateDate:       awsManagedPolicyCreated,
		UpdateDate:       awsManagedPolicyCreated,
		PolicyDocument:   awsManagedPolicyDocuments[name],
	}, true
}

// policyLocked returns the policy an ARN names: a customer-managed policy, or
// an AWS-managed one. With materialize set (the caller holds the write lock),
// an AWS-managed policy is stored on first use so its attachment count can
// be tracked.
func (s *MemoryStorage) policyLocked(arn string, materialize bool) (*Policy, bool) {
	if policy, ok := s.Policies[arn]; ok {
		return policy, true
	}

	policy, ok := awsManagedPolicy(arn)
	if ok && materialize {
		s.Policies[arn] = policy
	}

	return policy, ok
}
